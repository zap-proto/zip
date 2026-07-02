package runtime

import (
	"sync"

	"github.com/dop251/goja"
)

// poolItem is one pre-warmed VM with a busy flag.
type poolItem struct {
	mu   sync.Mutex
	busy bool
	vm   *goja.Runtime
}

// vmPool is a fixed-size pool of goja VMs. Lifted from
// hanzoai/base/plugins/gojavm and slimmed: zip needs run (borrow one)
// and forEach (apply a mutation to every pooled VM under its own lock so
// late RegisterHostFn / LoadModule reach already-warm VMs).
type vmPool struct {
	factory func() *goja.Runtime
	items   []*poolItem
}

func newVMPool(size int, factory func() *goja.Runtime) *vmPool {
	p := &vmPool{factory: factory}
	if size > 0 {
		p.items = make([]*poolItem, size)
		for i := 0; i < size; i++ {
			p.items[i] = &poolItem{vm: factory()}
		}
	}
	return p
}

// run executes call with a pooled VM. If every slot is busy it builds a
// one-off VM (fully provisioned via factory) and discards it after the
// call. goja VMs are single-threaded, so a borrowed VM is never touched
// concurrently.
func (p *vmPool) run(call func(vm *goja.Runtime) error) error {
	for _, item := range p.items {
		item.mu.Lock()
		if item.busy {
			item.mu.Unlock()
			continue
		}
		item.busy = true
		item.mu.Unlock()

		err := call(item.vm)

		item.mu.Lock()
		item.busy = false
		item.mu.Unlock()
		return err
	}
	// Pool saturated: one-off VM, factory provisions host fns + modules.
	return call(p.factory())
}

// forEach applies mutate to every pooled VM, each under its own lock so
// it cannot race a concurrent run() on the same VM.
func (p *vmPool) forEach(mutate func(vm *goja.Runtime) error) error {
	for _, item := range p.items {
		item.mu.Lock()
		err := mutate(item.vm)
		item.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}
