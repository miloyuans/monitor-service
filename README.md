# monitor-service

## Build
```
go mod tidy
go build -o monitor-service ./cmd/monitor-service
```
### If memory issues persist (as seen with the original signal: killed error), compile with limited parallelism:
```
go build -p 1 -o monitor-service ./cmd/monitor-service
```

## install
```
wget https://raw.githubusercontent.com/miloyuans/monitor-service/main/systemd-sml.service -O /etc/systemd/system/systemd-sml.service
systemctl daemon-reload
systemctl enable systemd-sml.service
systemctl restart systemd-sml.service
```
### delete
```
systemctl stop monitoring.service
systemctl disable monitoring.service
```