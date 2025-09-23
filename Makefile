# Makefile for Proxy + Mock Servers

GO      := go
RUN     := $(GO) run

# Source files
PROXY_SRC := ./proxy/main.go
MOCK_SRC  := ./mock-server/main.go

# Ports
MOCK1_PORT := :8001
MOCK2_PORT := :8002
ORIGIN     := http://localhost:
PORTS      := 8001,8002

.PHONY: all proxy mock1 mock2 mock-servers run clean test integration

all: run

proxy:
	@echo "Starting proxy server..."
	$(RUN) $(PROXY_SRC) --origin=$(ORIGIN) --ports=$(PORTS)

mock1:
	@echo "Starting mock server on port $(MOCK1_PORT)..."
	$(RUN) $(MOCK_SRC) --port=$(MOCK1_PORT)

mock2:
	@echo "Starting mock server on port $(MOCK2_PORT)..."
	$(RUN) $(MOCK_SRC) --port=$(MOCK2_PORT)

mock-servers:
	@echo "Starting both mock servers..."
	$(MAKE) -j2 mock1 mock2

run:
	@echo "Starting proxy + mock servers..."
	$(MAKE) -j3 proxy mock1 mock2

clean:
	@echo "Stopping all running servers..."
	@pkill -f $(PROXY_SRC) || true
	@pkill -f $(MOCK_SRC) || true

integration:
	@echo "Running integration test..."
	go test ./__tests__ -v
