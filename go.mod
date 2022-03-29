module helloworld

go 1.16

require (
	github.com/go-kratos/kratos/v2 v2.1.5
	github.com/google/wire v0.5.0
	github.com/opensergo/opensergo-go v0.0.0-20220329080036-bcdf0e962191 // indirect
	golang.org/x/net v0.0.0-20220127200216-cd36cc0744dd // indirect
	golang.org/x/sys v0.0.0-20220114195835-da31bd327af9 // indirect
	google.golang.org/genproto v0.0.0-20220126215142-9970aeb2e350
	google.golang.org/grpc v1.45.0
	google.golang.org/protobuf v1.27.1
)

replace github.com/go-kratos/kratos/v2 v2.1.5 => ./pkg-custom/github.com/go-kratos/kratos/v2/
