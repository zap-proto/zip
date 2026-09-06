# The build, in the order the facts become available.
#
# A proc-macro writes its fragment of the manifest as the crate compiles, so the
# description exists only after a compile — which is why projecting it is a
# second step and not a build script. What zipc writes is checked in beside the
# source, the way a generated file is anywhere else, so a fresh clone builds
# without either tool.
#
#   make gen    compile, project, and compile again against the projections
#   make run    serve on the default addresses
#   make check  the doors answer, and the wire is ZAP
#   make wire   the ZAP accessors for these ops, for a client that wants them

ZIPC   ?= zipc
ZAPGEN ?= zapgen
ROOT   := ../..

gen:
	cd $(ROOT) && cargo build -p info
	$(ZIPC) -out gen -package info target/zip
	cd $(ROOT) && cargo build -p info

wire:
	$(ZAPGEN) -lang rust -out $(OUT) gen/info.zap

run:
	cd $(ROOT) && cargo run -p info

check:
	cd $(ROOT) && cargo test -p info

OUT ?= wire
