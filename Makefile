BINARY    := repomap
CMD_PATH  := ./cmd/repomap
BUILD_DIR := bin

PREFIX  ?= /usr/local
BINDIR  := $(PREFIX)/bin

.PHONY: build install uninstall clean install-service

build:
	mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(BINARY) $(CMD_PATH)

install: build
	mkdir -p $(BINDIR)
	install -m 0755 $(BUILD_DIR)/$(BINARY) $(BINDIR)/$(BINARY)
	@echo "Installed $(BINARY) to $(BINDIR)/$(BINARY)"
	@echo "Make sure $(BINDIR) is on your PATH."

uninstall:
	rm -f $(BINDIR)/$(BINARY)

clean:
	rm -rf $(BUILD_DIR)

# Renders a daemon service file for the current OS with the installed
# binary path filled in. Writes the rendered file under dist/service/ —
# it does NOT copy it into a system service directory or load/enable it.
install-service: install
	scripts/install-service.sh "$(BINDIR)/$(BINARY)"
