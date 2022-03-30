# Kratos OpenSergo Demo

## 如何运行

首先，运行起来 [OpenSergo Dashboard](https://github.com/opensergo/opensergo-dashboard/blob/develop/README.zh-Hans.md)。

```shell
make build
cd bin
env OPENSERGO_BOOTSTRAP_CONFIG='{"endpoint":"localhost:9090"}' ./helloworld
```

然后就可以在 OpenSergo Dashboard 上看到对应的应用了。
