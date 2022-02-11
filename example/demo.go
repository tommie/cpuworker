// Copyright 2021 The cpuworker Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"crypto/rand"
	"fmt"
	"hash/crc32"
	_ "net/http/pprof"
	"os"
	"sync"

	mathrand "math/rand"
	"runtime"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
	"github.com/hnes/cpuworker"
)

var glCrc32bs = make([]byte, 1024*256)

func cpuIntensiveTask(amt int) uint32 {
	var ck uint32
	for i := 0; i < amt; i++ {
		ck = crc32.ChecksumIEEE(glCrc32bs)
	}
	return ck
}

func cpuIntensiveTaskWithCheckpoint(amt int, checkpointFp func()) uint32 {
	var ck uint32
	for i := 0; i < amt; i++ {
		ck = crc32.ChecksumIEEE(glCrc32bs)
		checkpointFp()
	}
	return ck
}

const checksumAmt = 100 //00

func checksumWithoutCpuWorker() {
	cpuIntensiveTask(checksumAmt + mathrand.Intn(checksumAmt))
}

func checksumWithCpuWorkerAndHasCheckpoint() {
	cpuworker.Submit1(func(checkpointFp func()) {
		cpuIntensiveTaskWithCheckpoint(checksumAmt+mathrand.Intn(checksumAmt), checkpointFp)
	}).Sync()
}

func checksumSmallTaskWithCpuWorker() {
	cpuworker.Submit(func() {
		cpuIntensiveTask(10)
	}).Sync()
}

func delay1ms() {
	<-time.After(1 * time.Millisecond)
}

func delayLoop() {
	for i := 0; i < 10; i++ {
		<-time.After(1 * time.Millisecond)
	}
}

func delayLoopWithCpuWorker() {
	cpuworker.Submit3(func(eventCall func(func())) {
		for i := 0; i < 10; i++ {
			eventCall(func() {
				<-time.After(1 * time.Millisecond)
			})
		}
	}, 0, true).Sync()
}

func runScenario(delayUS *hdrhistogram.Histogram, f func(), conc, count int) {
	tokenCh := make(chan struct{}, conc)
	for i := 0; i < conc; i++ {
		tokenCh <- struct{}{}
	}

	var wg sync.WaitGroup
	wg.Add(count)

	delayCh := make(chan int64, conc)
	go func() {
		defer close(delayCh)
		wg.Wait()
	}()

	go func() {
		for i := 0; i < count; i++ {
			<-tokenCh
			go func() {
				defer func() { tokenCh <- struct{}{} }()
				defer wg.Done()
				start := time.Now()
				f()
				delayCh <- int64(time.Now().Sub(start) / time.Microsecond)
			}()
		}
	}()

	for delay := range delayCh {
		delayUS.RecordValue(delay)
	}
}

func scenario1(count int) []*hdrhistogram.Histogram {
	fmt.Printf("Running scenario 1 (delay1ms) count=%d\n", count)

	delayUS := hdrhistogram.New(1, 10000000, 2)
	runScenario(delayUS, delay1ms, 100, count)

	return []*hdrhistogram.Histogram{delayUS}
}

func scenario2(count int) []*hdrhistogram.Histogram {
	fmt.Printf("Running scenario 2 (delay1ms and checksumWithoutCpuWorker) count=%d\n", count)

	var wg sync.WaitGroup
	wg.Add(2)

	delayUS1 := hdrhistogram.New(1, 10000000, 2)
	go func() {
		defer wg.Done()
		runScenario(delayUS1, delay1ms, 100, count)
	}()

	delayUS2 := hdrhistogram.New(1, 10000000, 2)
	go func() {
		defer wg.Done()
		runScenario(delayUS2, checksumWithoutCpuWorker, 100, count)
	}()

	wg.Wait()

	return []*hdrhistogram.Histogram{delayUS1}
}

func scenario3(count int) []*hdrhistogram.Histogram {
	fmt.Printf("Running scenario 3 (delay1ms and checksumWithCpuWorkerAndHasCheckpoint) count=%d\n", count)

	var wg sync.WaitGroup
	wg.Add(2)

	delayUS1 := hdrhistogram.New(1, 10000000, 2)
	go func() {
		defer wg.Done()
		runScenario(delayUS1, delay1ms, 100, count)
	}()

	delayUS2 := hdrhistogram.New(1, 10000000, 2)
	go func() {
		defer wg.Done()
		runScenario(delayUS2, checksumWithCpuWorkerAndHasCheckpoint, 100, count)
	}()

	wg.Wait()

	return []*hdrhistogram.Histogram{delayUS1}
}

func scenario4(count int) []*hdrhistogram.Histogram {
	fmt.Printf("Running scenario 4 (delay1ms, checksumWithCpuWorkerAndHasCheckpoint and checksumSmallTaskWithCpuWorker) count=%d\n", count)

	var wg sync.WaitGroup
	wg.Add(3)

	delayUS1 := hdrhistogram.New(1, 10000000, 2)
	go func() {
		defer wg.Done()
		runScenario(delayUS1, delay1ms, 100, count)
	}()

	delayUS2 := hdrhistogram.New(1, 10000000, 2)
	go func() {
		defer wg.Done()
		runScenario(delayUS2, checksumWithCpuWorkerAndHasCheckpoint, 100, count)
	}()

	delayUS3 := hdrhistogram.New(1, 10000000, 2)
	go func() {
		defer wg.Done()
		runScenario(delayUS3, checksumSmallTaskWithCpuWorker, 100, count)
	}()

	wg.Wait()

	return []*hdrhistogram.Histogram{delayUS1, delayUS3}
}

func main() {
	rand.Read(glCrc32bs)
	nCPU := runtime.GOMAXPROCS(0)
	cpuP := cpuworker.GetGlobalWorkers().GetMaxP()
	fmt.Println("GOMAXPROCS:", nCPU, "DefaultMaxTimeSlice:", cpuworker.DefaultMaxTimeSlice,
		"cpuWorkerMaxP:", cpuP, "length of crc32 bs:", len(glCrc32bs))

	for _, fun := range []func(int) []*hdrhistogram.Histogram{scenario1, scenario2, scenario3, scenario4} {
		fmt.Println()

		start := time.Now()
		histos := fun(100000 * cpuP / 12)

		for _, histo := range histos {
			histo.PercentilesPrint(os.Stdout, 1, 1)
		}
		fmt.Printf("Total time: %v\n", time.Now().Sub(start))
	}
}
