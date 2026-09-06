BIN := bin/linkedin-mcp
PKG := ./cmd/linkedin-mcp

.PHONY: help build install run test cover check vet fmt lint tidy clean

# help lists the targets, taken from the comment above each one.
help:
	@grep -E '^[a-z-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

build: ## Compile le binaire statique dans bin/
	@mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o $(BIN) $(PKG)

run: build ## Lance le serveur en local, en lisant .env
	@set -a; [ -f .env ] && . ./.env; set +a; $(BIN)

test: ## Joue toute la suite de tests
	go test ./...

cover: ## Joue les tests avec la couverture par paquet
	go test -cover ./internal/...

vet: ## go vet
	go vet ./...

fmt: ## Vérifie le formatage
	@test -z "$$(gofmt -l . | tee /dev/stderr)" || (echo "gofmt: fichiers non formatés"; exit 1)

lint: ## staticcheck, ignoré s'il est absent ou trop ancien
	@command -v staticcheck >/dev/null 2>&1 && staticcheck ./... || echo "staticcheck indisponible ou incompatible, ignoré"

check: fmt vet lint test ## La porte que chaque commit doit passer

tidy: ## Range go.mod
	go mod tidy

install: ## Installe le binaire dans /usr/local/bin
	CGO_ENABLED=0 go build -trimpath -o /usr/local/bin/linkedin-mcp $(PKG)

clean: ## Supprime bin/
	rm -rf bin
