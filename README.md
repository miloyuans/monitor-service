# monitor-service

##Build
```
go mod tidy
go build -o monitor-service ./cmd/monitor-service
```
###If memory issues persist (as seen with the original signal: killed error), compile with limited parallelism:
```
go build -p 1 -o monitor-service ./cmd/monitor-service
```