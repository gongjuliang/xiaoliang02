package AtomicFunc

import (
	"commonGin/Common/Var/AtomicVar"
	"sync/atomic"
)

func AtomicIntAddGet() int64 {
	return atomic.AddInt64(&AtomicVar.AtomicInt64, 1)
}
