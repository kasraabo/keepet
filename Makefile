.PHONY: setup run test generate
setup:
	./scripts/setup.sh
run:
	./scripts/start.sh
test:
	./scripts/test.sh
generate:
	bash -c 'source scripts/env.sh && go run github.com/a-h/templ/cmd/templ@v0.3.960 generate -f internal/ui/layout.templ'
