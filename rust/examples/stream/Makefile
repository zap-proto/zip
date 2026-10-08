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

# gen names a directory as well as a step, and the step is the one meant.
.PHONY: gen run check

# zipc is this checkout's, so the documents are projected by the projector
# the manifest was written for.
ZIPC   ?= go run ../../../cmd/zipc
ROOT   := ../..

gen:
	cd $(ROOT) && cargo run -q -p stream -- --manifest > examples/stream/gen/manifest.json
	$(ZIPC) -out gen -package stream gen/manifest.json
	cd $(ROOT) && cargo build -p stream

run:
	cd $(ROOT) && cargo run -p stream

check:
	cd $(ROOT) && cargo test -p stream
