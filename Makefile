.PHONY: setup run test generate release-check
setup:
	./scripts/setup.sh
run:
	./scripts/start.sh
test:
	./scripts/test.sh
generate:
	bash -c 'source scripts/env.sh && go run github.com/a-h/templ/cmd/templ@v0.3.960 generate -f internal/ui/layout.templ'

release-check:
	bash -c 'source scripts/env.sh && python3 scripts/release-check.py'
