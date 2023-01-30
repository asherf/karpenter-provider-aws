/*
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// batcher implements a generic batching lib for API calls so that load can be reduced to external APIs that excessively throttle
package batcher

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mitchellh/hashstructure/v2"
	"github.com/samber/lo"
)

// Options allows for configuration of the Batcher
type Options[T input, U output] struct {
	IdleTimeout   time.Duration
	MaxTimeout    time.Duration
	MaxItems      int
	RequestHasher RequestHasher[T]
	BatchExecutor BatchExecutor[T, U]
}

// Result is a container for the output and error of an execution
type Result[U output] struct {
	Output *U
	Err    error
}

type input = any
type output = any

// request is a batched request with the calling ctx, requestor, and hash to determine the batching bucket
type request[T input, U output] struct {
	ctx       context.Context
	hash      uint64
	input     *T
	requestor chan Result[U]
}

// Batcher is used to batch API calls with identical parameters into a single call
type Batcher[T input, U output] struct {
	ctx      context.Context
	mu       sync.Mutex
	trigger  chan struct{}
	requests map[uint64][]*request[T, U]
	options  Options[T, U]
}

// BatchExecutor is a function that executes a slice of inputs against the batched API.
// inputs will be mutated
// The returned Result slice is expected to match the len of the input slice and be in the
// same order, if order matters for the batched API
type BatchExecutor[T input, U output] func(ctx context.Context, input []*T) []Result[U]

// RequestHasher is a function that hashes input to bucket inputs into distinct batches
type RequestHasher[T input] func(ctx context.Context, input *T) uint64

// NewBatcher creates a batcher that can batch a particular input and output type
func NewBatcher[T input, U output](ctx context.Context, options Options[T, U]) *Batcher[T, U] {
	b := &Batcher[T, U]{
		ctx:      ctx,
		requests: map[uint64][]*request[T, U]{},
		trigger:  make(chan struct{}),
		options:  options,
	}
	go b.run()
	return b
}

// Add will add an input to the batcher using the batcher's hashing function
func (b *Batcher[T, U]) Add(ctx context.Context, input *T) Result[U] {
	request := &request[T, U]{
		ctx:   ctx,
		hash:  b.options.RequestHasher(ctx, input),
		input: input,
		// The requestor channel is buffered to ensure that the exec runner can always write the result out preventing
		// any single caller from blocking the others. Specifically since we register our request and then trigger, the
		// request may be processed while the triggering blocks.
		requestor: make(chan Result[U], 1),
	}
	b.mu.Lock()
	b.requests[request.hash] = append(b.requests[request.hash], request)
	b.mu.Unlock()
	b.trigger <- struct{}{}
	return <-request.requestor
}

// DefaultHasher will hash the entire input
func DefaultHasher[T input](ctx context.Context, input *T) uint64 {
	hash, err := hashstructure.Hash(input, hashstructure.FormatV2, &hashstructure.HashOptions{SlicesAsSets: true})
	if err != nil {
		panic("error hashing")
	}
	return hash
}

// OneBucketHasher will return a constant hash and should be used when there is only one type of request
func OneBucketHasher[T input](ctx context.Context, input *T) uint64 {
	return 0
}

func (b *Batcher[T, U]) run() {
	for {
		select {
		// context that we started with has completed so the app is shutting down
		case <-b.ctx.Done():
			return
		case <-b.trigger:
			// wait to start the batch of create fleet calls
		}
		b.waitForIdle()
		b.runCalls()
	}
}

func (b *Batcher[T, U]) waitForIdle() {
	timeout := time.NewTimer(b.options.MaxTimeout)
	idle := time.NewTimer(b.options.IdleTimeout)
	count := 0
	for {
		select {
		case <-b.trigger:
			count++
			if b.options.MaxItems != 0 && count >= b.options.MaxItems {
				return
			}
			if !idle.Stop() {
				<-idle.C
			}
			idle.Reset(b.options.IdleTimeout)
		case <-timeout.C:
			return
		case <-idle.C:
			return
		}
	}
}

func (b *Batcher[T, U]) runCalls() {
	b.mu.Lock()
	defer b.mu.Unlock()
	requestMap := b.requests
	b.requests = map[uint64][]*request[T, U]{}

	for _, requestBatch := range requestMap {
		requestIdx := 0
		for _, result := range b.options.BatchExecutor(requestBatch[0].ctx, lo.Map(requestBatch, func(req *request[T, U], _ int) *T { return req.input })) {
			requestBatch[requestIdx].requestor <- result
			requestIdx++
		}

		// any unmapped outputs should return an error to the caller
		for ; requestIdx < len(requestBatch); requestIdx++ {
			requestBatch[requestIdx].requestor <- Result[U]{Err: fmt.Errorf("error making call")}
		}
	}
}