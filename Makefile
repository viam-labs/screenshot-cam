BIN_OUTPUT_PATH = bin
TOOL_BIN = bin/gotools/$(shell uname -s)-$(shell uname -m)
UNAME_S ?= $(shell uname -s)
GOPATH = $(HOME)/go/bin
export PATH := ${PATH}:$(GOPATH) 

build: format update-rdk
	rm -f $(BIN_OUTPUT_PATH)/screenshot-cam
	go build $(LDFLAGS) -o $(BIN_OUTPUT_PATH)/screenshot-cam main.go

module.tar.gz: build
	rm -f $(BIN_OUTPUT_PATH)/module.tar.gz
	tar czf $(BIN_OUTPUT_PATH)/module.tar.gz $(BIN_OUTPUT_PATH)/screenshot-cam

windows:
	GOOS=windows GOARCH=amd64 go build -tags no_cgo -ldflags="-s -w" .
	rm -f module.tar.gz
	tar czf module.tar.gz screenshot-cam.exe meta.json

setup: 
	if [ "$(UNAME_S)" = "Linux" ]; then \
		sudo apt-get install -y apt-utils coreutils tar libnlopt-dev libjpeg-dev pkg-config; \
	fi
	# remove unused imports
	go install golang.org/x/tools/cmd/goimports@latest
	find . -name '*.go' -exec $(GOPATH)/goimports -w {} +


clean:
	rm -rf $(BIN_OUTPUT_PATH)/screenshot-cam $(BIN_OUTPUT_PATH)/module.tar.gz screenshot-cam

format:
	gofmt -w -s .
	
update-rdk:
	go get go.viam.com/rdk@latest
	go mod tidy
