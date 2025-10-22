package main

/*
#include <stdlib.h>
typedef void (*netx_callback_t)(const char* msg);
extern int Netx(char* id, int argc, char** argv, netx_callback_t outCB, netx_callback_t errCB);
extern int NetxInterrupt(char* handle);
*/
import "C"

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"unsafe"
)

const handle = "e2e"

func main() {
	ctx, _ := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		interrupt()
	}()
	os.Exit(run())

}

func run() int {
	cHandle := C.CString(handle)
	defer C.free(unsafe.Pointer(cHandle))
	args := os.Args[1:]
	argc := C.int(len(args))
	var argv **C.char
	if len(args) > 0 {
		cArgs := make([]*C.char, len(args))
		for i, arg := range args {
			cArgs[i] = C.CString(arg)
		}
		defer func() {
			for _, cArg := range cArgs {
				C.free(unsafe.Pointer(cArg))
			}
		}()
		argv = (**C.char)(unsafe.Pointer(&cArgs[0]))
	}
	code := C.Netx(cHandle, argc, argv, nil, nil)
	return int(code)
}

func interrupt() int {
	cHandle := C.CString(handle)
	defer C.free(unsafe.Pointer(cHandle))
	return int(C.NetxInterrupt(cHandle))
}
