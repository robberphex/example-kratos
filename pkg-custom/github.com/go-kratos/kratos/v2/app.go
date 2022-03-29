package kratos

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/registry"
	"github.com/go-kratos/kratos/v2/transport"
	"github.com/go-kratos/kratos/v2/transport/grpc"
	service_contract_v1 "github.com/opensergo/opensergo-go/proto/service_contract/v1"
	google_grpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

// AppInfo is application context value.
type AppInfo interface {
	ID() string
	Name() string
	Version() string
	Metadata() map[string]string
	Endpoint() []string
}

// App is an application components lifecycle manager.
type App struct {
	opts     options
	ctx      context.Context
	cancel   func()
	lk       sync.Mutex
	instance *registry.ServiceInstance
}

// New create an application lifecycle manager.
func New(opts ...Option) *App {
	o := options{
		ctx:              context.Background(),
		logger:           log.NewHelper(log.GetLogger()),
		sigs:             []os.Signal{syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGINT},
		registrarTimeout: 10 * time.Second,
		stopTimeout:      10 * time.Second,
	}
	if id, err := uuid.NewUUID(); err == nil {
		o.id = id.String()
	}
	for _, opt := range opts {
		opt(&o)
	}
	ctx, cancel := context.WithCancel(o.ctx)
	return &App{
		ctx:    ctx,
		cancel: cancel,
		opts:   o,
	}
}

type openSergoConfig struct {
	Endpoint string `json:"endpoint"`
}

// ID returns app instance id.
func (a *App) ID() string { return a.opts.id }

// Name returns service name.
func (a *App) Name() string { return a.opts.name }

// Version returns app version.
func (a *App) Version() string { return a.opts.version }

// Metadata returns service metadata.
func (a *App) Metadata() map[string]string { return a.opts.metadata }

// Endpoint returns endpoints.
func (a *App) Endpoint() []string {
	if a.instance == nil {
		return []string{}
	}
	return a.instance.Endpoints
}

// parseMetadata finds the file descriptor bytes specified meta.
// For SupportPackageIsVersion4, m is the name of the proto file, we
// call proto.FileDescriptor to get the byte slice.
// For SupportPackageIsVersion3, m is a byte slice itself.
func parseMetadata(meta interface{}) (protoreflect.FileDescriptor, error) {
	// Check if meta is the file name.
	if fileNameForMeta, ok := meta.(string); ok {
		return protoregistry.GlobalFiles.FindFileByPath(fileNameForMeta)
	}
	return nil, nil
}

// Run executes all OnStart hooks registered with the application's Lifecycle.
func (a *App) Run() error {
	instance, err := a.buildInstance()
	if err != nil {
		return err
	}
	ctx := NewContext(a.ctx, a)
	eg, ctx := errgroup.WithContext(ctx)
	wg := sync.WaitGroup{}

	for _, srv := range a.opts.servers {
		eg.Go(func() error {
			<-ctx.Done() // wait for stop signal
			sctx, cancel := context.WithTimeout(NewContext(context.Background(), a), a.opts.stopTimeout)
			defer cancel()
			return srv.Stop(sctx)
		})
		wg.Add(1)
		eg.Go(func() error {
			wg.Done()
			return srv.Start(ctx)
		})
	}
	wg.Wait()
	reportMetadata(a, instance)
	if a.opts.registrar != nil {
		rctx, rcancel := context.WithTimeout(a.opts.ctx, a.opts.registrarTimeout)
		defer rcancel()
		if err := a.opts.registrar.Register(rctx, instance); err != nil {
			return err
		}
		a.lk.Lock()
		a.instance = instance
		a.lk.Unlock()
	}
	c := make(chan os.Signal, 1)
	signal.Notify(c, a.opts.sigs...)
	eg.Go(func() error {
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-c:
				err := a.Stop()
				if err != nil {
					a.opts.logger.Errorf("failed to stop app: %v", err)
					return err
				}
			}
		}
	})
	if err := eg.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func reportMetadata(a *App, instance *registry.ServiceInstance) {
	var services []*service_contract_v1.ServiceDescriptor
	for _, srv := range a.opts.servers {
		srv := srv

		if g, ok := srv.(*grpc.Server); ok {
			serviceInfo := g.Server.GetServiceInfo()
			serviceNames := make([]string, 0, len(serviceInfo))

			for svc, info := range serviceInfo {
				serviceNames = append(serviceNames, svc)
				serviceDesc, err := processServiceInfo(svc, info)
				if err != nil {
					continue
				}
				services = append(services, serviceDesc)
			}
		}
	}
	serviceContract := service_contract_v1.ServiceContract{
		Services: services,
	}

	var addrs []*service_contract_v1.SocketAddress
	for _, endpoint := range instance.Endpoints {
		u, err := url.Parse(endpoint)
		if err != nil {
			log.Warnf("err: %v", err)
		}
		if u.Scheme == "grpc" {
			host, port, err := net.SplitHostPort(u.Host)
			if err != nil {
				log.Warnf("err: %v", err)
			} else {
				portValue, err := strconv.Atoi(port)
				if err != nil {
					log.Warnf("err: %v", err)
				}
				addrs = append(addrs, &service_contract_v1.SocketAddress{
					Address:   host,
					PortValue: uint32(portValue),
				})
			}
		}
	}

	serviceMetadata := service_contract_v1.ServiceMetadata{
		ServiceContract:    &serviceContract,
		Protocols:          []string{"grpc"},
		ListeningAddresses: addrs,
	}
	ose := getOpenSergoEndpoint()
	timeoutCtx, _ := context.WithTimeout(context.TODO(), 10*time.Second)
	conn, err := google_grpc.DialContext(timeoutCtx, ose, google_grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Warnf("err: %v", err)
	}
	mClient := service_contract_v1.NewMetadataServiceClient(conn)
	reply, err := mClient.ReportMetadata(context.TODO(), &service_contract_v1.ReportMetadataRequest{
		AppName:         a.Name(),
		ServiceMetadata: []*service_contract_v1.ServiceMetadata{&serviceMetadata},
	})
	_ = reply
}

func processServiceInfo(name string, serviceInfo google_grpc.ServiceInfo) (*service_contract_v1.ServiceDescriptor, error) {
	var methods []*service_contract_v1.MethodDescriptor
	fd, err := parseMetadata(serviceInfo.Metadata)
	if err != nil {
		return nil, err
	}
	//fd.Options()
	for i := 0; i < fd.Services().Len(); i++ {
		sd := fd.Services().Get(i)
		if sd.FullName() != protoreflect.FullName(name) {
			continue
		}
		for j := 0; j < sd.Methods().Len(); j++ {
			md := sd.Methods().Get(i)
			mName := string(md.Name())
			inputType := string(md.Input().FullName())
			outputType := string(md.Output().FullName())
			methodDesc := service_contract_v1.MethodDescriptor{
				Name:        mName,
				InputTypes:  []string{inputType},
				OutputTypes: []string{outputType},
			}
			methods = append(methods, &methodDesc)
			// process Type
		}
	}

	serviceDesc := service_contract_v1.ServiceDescriptor{
		Name:    name,
		Methods: methods,
	}
	return &serviceDesc, nil
}

func getOpenSergoEndpoint() string {
	config_str := os.Getenv("OPENSERGO_BOOTSTRAP_CONFIG")
	config := openSergoConfig{}
	err := json.Unmarshal([]byte(config_str), &config)
	if err != nil {
		panic(err)
	}
	return config.Endpoint
}

// Stop gracefully stops the application.
func (a *App) Stop() error {
	a.lk.Lock()
	instance := a.instance
	a.lk.Unlock()
	if a.opts.registrar != nil && instance != nil {
		ctx, cancel := context.WithTimeout(a.opts.ctx, a.opts.registrarTimeout)
		defer cancel()
		if err := a.opts.registrar.Deregister(ctx, instance); err != nil {
			return err
		}
	}
	if a.cancel != nil {
		a.cancel()
	}
	return nil
}

func (a *App) buildInstance() (*registry.ServiceInstance, error) {
	endpoints := make([]string, 0) //nolint:gomnd
	for _, e := range a.opts.endpoints {
		endpoints = append(endpoints, e.String())
	}
	if len(endpoints) == 0 {
		for _, srv := range a.opts.servers {
			if r, ok := srv.(transport.Endpointer); ok {
				e, err := r.Endpoint()
				if err != nil {
					return nil, err
				}
				endpoints = append(endpoints, e.String())
			}
		}
	}
	return &registry.ServiceInstance{
		ID:        a.opts.id,
		Name:      a.opts.name,
		Version:   a.opts.version,
		Metadata:  a.opts.metadata,
		Endpoints: endpoints,
	}, nil
}

type appKey struct{}

// NewContext returns a new Context that carries value.
func NewContext(ctx context.Context, s AppInfo) context.Context {
	return context.WithValue(ctx, appKey{}, s)
}

// FromContext returns the Transport value stored in ctx, if any.
func FromContext(ctx context.Context) (s AppInfo, ok bool) {
	s, ok = ctx.Value(appKey{}).(AppInfo)
	return
}
