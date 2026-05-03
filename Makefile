.DEFAULT_GOAL := all
.NOTPARALLEL:

HOST_GOOS := $(shell go env GOHOSTOS)
NODE_BIN := node

SUPPORTED_PLATFORMS := windows linux termux
SUPPORTED_ARCH_windows := amd64
SUPPORTED_ARCH_linux := amd64 arm_hf arm64
SUPPORTED_ARCH_termux := arm64

PACKAGED_WEBUI_DIST_DIR := ../webui

ifeq ($(HOST_GOOS),windows)
HOST_PLATFORM := windows
else ifeq ($(HOST_GOOS),linux)
HOST_PLATFORM := linux
else ifeq ($(HOST_GOOS),android)
HOST_PLATFORM := termux
else
HOST_PLATFORM :=
endif

PLATFORM ?= $(HOST_PLATFORM)

ifeq ($(strip $(PLATFORM)),)
$(error PLATFORM is required. Supported platforms: $(SUPPORTED_PLATFORMS))
endif

ifeq ($(filter $(PLATFORM),$(SUPPORTED_PLATFORMS)),)
$(error unsupported PLATFORM '$(PLATFORM)'. Supported platforms: $(SUPPORTED_PLATFORMS))
endif

ifeq ($(PLATFORM),windows)
DEFAULT_ARCH := amd64
GOOS := windows
GOARCH := amd64
GOARM :=
EXE_EXT := .exe
ARCHIVE_EXT := zip
else ifeq ($(PLATFORM),linux)
DEFAULT_ARCH := amd64
GOOS := linux
EXE_EXT :=
ARCHIVE_EXT := tar.gz
ifeq ($(ARCH),arm64)
GOARCH := arm64
GOARM :=
else ifeq ($(ARCH),arm_hf)
GOARCH := arm
GOARM := 7
else
GOARCH := amd64
GOARM :=
endif
else ifeq ($(PLATFORM),termux)
DEFAULT_ARCH := arm64
GOOS := android
GOARCH := arm64
GOARM :=
EXE_EXT :=
ARCHIVE_EXT := tar.gz
endif

ARCH ?= $(DEFAULT_ARCH)

ifeq ($(strip $(ARCH)),)
$(error ARCH is required for PLATFORM '$(PLATFORM)'. Supported values: $(SUPPORTED_ARCH_$(PLATFORM)))
endif

ifeq ($(filter $(ARCH),$(SUPPORTED_ARCH_$(PLATFORM))),)
$(error unsupported ARCH '$(ARCH)' for PLATFORM '$(PLATFORM)'. Supported values: $(SUPPORTED_ARCH_$(PLATFORM)))
endif

TARGET_NAME := frp-$(PLATFORM)-$(ARCH)
OUT_DIR := build/$(TARGET_NAME)
FRPS_BIN := $(OUT_DIR)/frps$(EXE_EXT)
FRPC_BIN := $(OUT_DIR)/frpc$(EXE_EXT)
WEBUI_DIR := $(OUT_DIR)/webui
DATA_DIR := $(OUT_DIR)/data
CONFIG_TEMPLATE := $(DATA_DIR)/config.json

ifeq ($(ARCHIVE_EXT),zip)
ARCHIVE := build/$(TARGET_NAME).zip
else
ARCHIVE := build/$(TARGET_NAME).tar.gz
endif

ifeq ($(HOST_GOOS),windows)
SHELL := cmd.exe
.SHELLFLAGS := /C
NPM_BIN := npm.cmd
ifeq ($(strip $(GOARM)),)
GO_BUILD_ENV_CMD := set "GOOS=$(GOOS)" && set "GOARCH=$(GOARCH)" &&
else
GO_BUILD_ENV_CMD := set "GOOS=$(GOOS)" && set "GOARCH=$(GOARCH)" && set "GOARM=$(GOARM)" &&
endif
else
SHELL := /bin/sh
.SHELLFLAGS := -ec
NPM_BIN := npm
PYTHON_BIN := $(shell if command -v python3 >/dev/null 2>&1; then printf '%s' python3; else printf '%s' python; fi)
ifeq ($(strip $(GOARM)),)
GO_BUILD_ENV_CMD := GOOS=$(GOOS) GOARCH=$(GOARCH)
else
GO_BUILD_ENV_CMD := GOOS=$(GOOS) GOARCH=$(GOARCH) GOARM=$(GOARM)
endif
endif

.PHONY: all init package clean clean-target prepare init-frps init-frpc init-webui build-frps build-frpc build-webui build-config create-archive print-target

all: clean-target prepare build-frps build-frpc build-webui build-config

init: init-frps init-frpc init-webui

package: all create-archive

print-target:
	@echo PLATFORM=$(PLATFORM)
	@echo ARCH=$(ARCH)
	@echo GOOS=$(GOOS)
	@echo GOARCH=$(GOARCH)
	@echo GOARM=$(GOARM)
	@echo OUT_DIR=$(OUT_DIR)
	@echo ARCHIVE=$(ARCHIVE)

build-config:
	@echo Preparing frps config template
	@$(NODE_BIN) -e "const fs=require('fs'); const dir='$(DATA_DIR)'; const dst='$(CONFIG_TEMPLATE)'; const cfg=JSON.parse(fs.readFileSync('frps/data/config.json','utf8')); cfg.webui.dist_dir='$(PACKAGED_WEBUI_DIST_DIR)'; fs.mkdirSync(dir,{recursive:true}); fs.writeFileSync(dst, JSON.stringify(cfg, null, 2) + '\n');"

ifeq ($(HOST_GOOS),windows)

clean-target:
	@echo Cleaning $(OUT_DIR)
	@powershell -NoProfile -Command "if (Test-Path '$(OUT_DIR)') { Remove-Item -LiteralPath '$(OUT_DIR)' -Recurse -Force }"

prepare:
	@echo Preparing $(OUT_DIR)
	@powershell -NoProfile -Command "New-Item -ItemType Directory -Force -Path '$(OUT_DIR)' | Out-Null"

init-frps:
	@echo Tidying frps module
	@cd /D frps && go mod tidy

init-frpc:
	@echo Tidying frpc module
	@cd /D frpc && go mod tidy

init-webui:
	@echo Installing webui dependencies
	@cd /D frps\webui && $(NPM_BIN) install

build-frps:
	@echo Building frps for $(PLATFORM)/$(ARCH)
	@cd /D frps && $(GO_BUILD_ENV_CMD) go build -o ../$(FRPS_BIN) ./cmd/frps

build-frpc:
	@echo Building frpc for $(PLATFORM)/$(ARCH)
	@cd /D frpc && $(GO_BUILD_ENV_CMD) go build -o ../$(FRPC_BIN) ./cmd/frpc

build-webui:
	@echo Building webui
	@cd /D frps\webui && if not exist node_modules ( $(NPM_BIN) ci )
	@cd /D frps\webui && $(NPM_BIN) run build
	@powershell -NoProfile -Command "$$dst = '$(WEBUI_DIR)'; if (Test-Path $$dst) { Remove-Item -LiteralPath $$dst -Recurse -Force }; New-Item -ItemType Directory -Force -Path $$dst | Out-Null; Copy-Item -Path 'frps/webui/dist/*' -Destination $$dst -Recurse -Force"

ifeq ($(PLATFORM),windows)
create-archive:
	@echo Packaging $(ARCHIVE)
	@powershell -NoProfile -Command "if (Test-Path '$(ARCHIVE)') { Remove-Item -LiteralPath '$(ARCHIVE)' -Force }; Compress-Archive -Path '$(OUT_DIR)' -DestinationPath '$(ARCHIVE)' -CompressionLevel Optimal"
else
create-archive:
	@echo Packaging $(ARCHIVE)
	@powershell -NoProfile -Command "if (Test-Path '$(ARCHIVE)') { Remove-Item -LiteralPath '$(ARCHIVE)' -Force }"
	@tar -czf "$(ARCHIVE)" -C build "$(TARGET_NAME)"
endif

clean:
	@echo Cleaning build
	@powershell -NoProfile -Command "if (Test-Path 'build') { Remove-Item -LiteralPath 'build' -Recurse -Force }"

else

clean-target:
	@echo "Cleaning $(OUT_DIR)"
	@rm -rf "$(OUT_DIR)"

prepare:
	@echo "Preparing $(OUT_DIR)"
	@mkdir -p "$(OUT_DIR)"

init-frps:
	@echo "Tidying frps module"
	@cd frps && go mod tidy

init-frpc:
	@echo "Tidying frpc module"
	@cd frpc && go mod tidy

init-webui:
	@echo "Installing webui dependencies"
	@cd frps/webui && $(NPM_BIN) install

build-frps:
	@echo "Building frps for $(PLATFORM)/$(ARCH)"
	@cd frps && $(GO_BUILD_ENV_CMD) go build -o ../$(FRPS_BIN) ./cmd/frps

build-frpc:
	@echo "Building frpc for $(PLATFORM)/$(ARCH)"
	@cd frpc && $(GO_BUILD_ENV_CMD) go build -o ../$(FRPC_BIN) ./cmd/frpc

build-webui:
	@echo "Building webui"
	@cd frps/webui && if [ ! -d node_modules ]; then $(NPM_BIN) ci; fi
	@cd frps/webui && $(NPM_BIN) run build
	@rm -rf "$(WEBUI_DIR)"
	@mkdir -p "$(WEBUI_DIR)"
	@cp -R frps/webui/dist/. "$(WEBUI_DIR)/"

ifeq ($(PLATFORM),windows)
create-archive:
	@echo "Packaging $(ARCHIVE)"
	@rm -f "$(ARCHIVE)"
	@cd build && $(PYTHON_BIN) -m zipfile -c "$(notdir $(ARCHIVE))" "$(TARGET_NAME)"
else
create-archive:
	@echo "Packaging $(ARCHIVE)"
	@rm -f "$(ARCHIVE)"
	@tar -czf "$(ARCHIVE)" -C build "$(TARGET_NAME)"
endif

clean:
	@echo "Cleaning build"
	@rm -rf build

endif
