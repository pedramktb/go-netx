package main

/*
#include <stdlib.h>
typedef void (*netx_callback_t)(const char* msg);
static inline void netx_call_callback(netx_callback_t cb, const char* msg) {
    if (cb != NULL) {
        cb(msg);
    }
}
*/
import "C"

import (
	"context"
	"os"
	"sync"
	"unsafe"

	"github.com/pedramktb/go-netx/cli/internal"
	_ "github.com/pedramktb/go-netx/drivers/aesgcm"
	_ "github.com/pedramktb/go-netx/drivers/dnst"
	_ "github.com/pedramktb/go-netx/drivers/dtls"
	_ "github.com/pedramktb/go-netx/drivers/dtlspsk"
	_ "github.com/pedramktb/go-netx/drivers/ssh"
	_ "github.com/pedramktb/go-netx/drivers/tls"
	_ "github.com/pedramktb/go-netx/drivers/tlspsk"
	_ "github.com/pedramktb/go-netx/drivers/utls"
)

func main() {}

type outWriter struct {
	cb C.netx_callback_t
}

func (w *outWriter) Write(p []byte) (int, error) {
	if len(p) != 0 && w.cb != nil {
		msg := C.CString(string(p))
		defer C.free(unsafe.Pointer(msg))
		C.netx_call_callback(w.cb, msg)
	}
	return os.Stdout.Write(p)
}

type errWriter struct {
	cb C.netx_callback_t
}

func (w *errWriter) Write(p []byte) (int, error) {
	if len(p) != 0 && w.cb != nil {
		msg := C.CString(string(p))
		defer C.free(unsafe.Pointer(msg))
		C.netx_call_callback(w.cb, msg)
	}
	return os.Stderr.Write(p)
}

type activeEntry struct {
	cancel context.CancelFunc
}

var (
	activeMu    sync.RWMutex
	activeCalls = make(map[string]*activeEntry)
)

func registerActiveCall(id string, cancel context.CancelFunc) *activeEntry {
	entry := &activeEntry{cancel: cancel}
	activeMu.Lock()
	prev := activeCalls[id]
	activeCalls[id] = entry
	activeMu.Unlock()
	if prev != nil && prev.cancel != nil {
		prev.cancel()
	}
	return entry
}

func removeActiveCall(id string, entry *activeEntry) {
	activeMu.Lock()
	if activeCalls[id] == entry {
		delete(activeCalls, id)
	}
	activeMu.Unlock()
}

func interruptActiveCall(id string) bool {
	activeMu.Lock()
	entry, ok := activeCalls[id]
	if ok {
		delete(activeCalls, id)
	}
	activeMu.Unlock()
	if ok && entry.cancel != nil {
		entry.cancel()
	}
	return ok
}

//export Netx
func Netx(id *C.char, argc C.int, argv **C.char, outCB, errCB C.netx_callback_t) C.int {
	var parsedArgs []string
	if argc > 0 && argv != nil {
		length := int(argc)
		argPtrs := (*[1 << 28]*C.char)(unsafe.Pointer(argv))[:length:length]
		parsedArgs = make([]string, 0, length)
		for _, argPtr := range argPtrs {
			if argPtr == nil {
				break
			}
			parsedArgs = append(parsedArgs, C.GoString(argPtr))
		}
	}

	eID := C.GoString(id)
	ctx, cancel := context.WithCancel(context.Background())
	entry := registerActiveCall(eID, cancel)
	defer func() {
		removeActiveCall(eID, entry)
		cancel()
	}()

	return C.int(internal.Run(
		ctx,
		cancel,
		internal.WithArgs(parsedArgs),
		internal.WithOut(&outWriter{cb: outCB}),
		internal.WithErr(&errWriter{cb: errCB}),
	))
}

//export NetxInterrupt
func NetxInterrupt(id *C.char) C.int {
	if id == nil {
		return 0
	}
	if interruptActiveCall(C.GoString(id)) {
		return 1
	}
	return 0
}
