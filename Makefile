
kubersync: main.go
	go build -o $@ main.go

kubersync.linux.amd64: main.go
	GOOS=linux GOARCH=amd64 go build -o $@ main.go
