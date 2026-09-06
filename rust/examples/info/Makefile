# The build, in the order the facts become available.
#
# The macro compiles what it read into the crate, and the binary prints it. So
# the manifest describes THIS build — there is no file beside the source to fall
# out of step, and no way to project a service that no longer exists.
#
# It is two steps and not a build script because the description exists only
# after a compile. What zipc writes is checked in beside the source, the way a
# generated file is anywhere else, so a fresh clone builds without either tool.
#
#   make gen    compile, project, and compile again against the projections
#   make run    serve on the default addresses
#   make check  the doors answer, and the wire is ZAP
#   make wire   the ZAP accessors for these ops, for a client that wants them

# gen names a directory as well as a step, and the step is the one meant.
.PHONY: gen run check wire

ZIPC   ?= zipc
ZAPGEN ?= zapgen
ROOT   := ../..
OUT    ?= wire

gen:
	cd $(ROOT) && cargo run -q -p info -- --manifest > examples/info/gen/manifest.json
	$(ZIPC) -out gen -package info gen/manifest.json
	cd $(ROOT) && cargo build -p info

run:
	cd $(ROOT) && cargo run -p info

check:
	cd $(ROOT) && cargo test -p info

wire:
	$(ZAPGEN) -lang rust -out $(OUT) gen/info.zap
