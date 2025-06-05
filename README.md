# Touka 框架

## 中间件

### 内置

Recovery `r.Use(Recovery())`
Touka Gzip `r.Use(Gzip(-1))`

### fenthope

[访问日志-record](https://github.com/fenthope/record) 
[Gzip](https://github.com/fenthope/gzip)
[压缩-Compress(Deflate,Gzip,Zstd)](https://github.com/fenthope/compress)
[请求速率限制-ikumi](https://github.com/fenthope/ikumi)
[sessions](https://github.com/fenthope/sessions)
[jwt](https://github.com/fenthope/jwt)
[带宽限制](https://github.com/fenthope/toukautil/blob/main/bandwithlimiter.go)

## 许可证

本项目在v0阶段使用WJQSERVER STUDIO LICENSE许可证, 后续进行调整

tree部分来自[gin](https://github.com/gin-gonic/gin)与[httprouter](https://github.com/julienschmidt/httprouter)

[WJQSERVER/httproute](https://github.com/WJQSERVER/httprouter)是本项目的前身(一个[httprouter](https://github.com/julienschmidt/httprouter)的fork版本)