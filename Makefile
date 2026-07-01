.PHONY: graph

# make graph PKG=internal/snapshot [OUT=path.svg]
graph:
	@test -n "$(PKG)" || (echo "usage: make graph PKG=<path-substring> [OUT=out.svg]" && exit 1)
	python3 scripts/codegraph_render.py "$(PKG)" $(OUT)
