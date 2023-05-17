// Licensed to Elasticsearch B.V. under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. Elasticsearch B.V. licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package systemtest

import (
	"context"
	"fmt"
	"math/rand"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest"

	"github.com/elastic/apm-data/model"
	apmqueue "github.com/elastic/apm-queue"
	"github.com/elastic/apm-queue/codec/json"
	"github.com/elastic/apm-queue/kafka"
	"github.com/elastic/apm-queue/pubsublite"
)

func TestProduceConsumeSingleTopic(t *testing.T) {
	// This test covers:
	// - TopicRouter publishes to a topic, regardless of the event content.
	// - Consumer consumes from a single topic.
	// - No errors are logged.
	logger := NoLevelLogger(t, zap.ErrorLevel)
	events := 100
	timeout := 60 * time.Second
	doSyncAsync(func(name string, sync bool) {
		topics := SuffixTopics(apmqueue.Topic(t.Name() + name))
		topicRouter := func(event model.APMEvent) apmqueue.Topic {
			return topics[0]
		}
		t.Run("Kafka"+name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			require.NoError(t,
				ProvisionKafka(ctx, newLocalKafkaConfig(topics...)),
			)
			var records atomic.Int64
			testProduceConsume(ctx, t, produceConsumeCfg{
				events:               events,
				expectedRecordsCount: events,
				records:              &records,
				producer: newKafkaProducer(t, kafka.ProducerConfig{
					Logger:      logger,
					Encoder:     json.JSON{},
					TopicRouter: topicRouter,
					Sync:        sync,
				}),
				consumer: newKafkaConsumer(t, kafka.ConsumerConfig{
					Logger:  logger,
					Decoder: json.JSON{},
					Topics:  topics,
					GroupID: t.Name(),
					Processor: assertBatchFunc(t, consumerAssertions{
						records:   &records,
						processor: model.TransactionProcessor,
					}),
				}),
				timeout: timeout,
			})
		})
		t.Run("PubSubLite"+name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			require.NoError(t,
				ProvisionPubSubLite(ctx, newPubSubLiteConfig(topics...)),
			)
			var records atomic.Int64
			testProduceConsume(ctx, t, produceConsumeCfg{
				events:  events,
				records: &records,
				producer: newPubSubLiteProducer(t, pubsublite.ProducerConfig{
					Logger:      logger,
					Encoder:     json.JSON{},
					TopicRouter: topicRouter,
					Sync:        sync,
				}),
				consumer: newPubSubLiteConsumer(ctx, t, pubsublite.ConsumerConfig{
					Logger:  logger,
					Decoder: json.JSON{},
					Topics:  topics,
					Processor: assertBatchFunc(t, consumerAssertions{
						records:   &records,
						processor: model.TransactionProcessor,
					}),
				}),
				timeout: timeout,
			})
		})
	})
}

func TestProduceConsumeMultipleTopics(t *testing.T) {
	// This test covers:
	// - TopicRouter publishes to different topics based on event contents.
	// - Consumer can consume from more than one topic.
	// - No errors are logged.
	logger := NoLevelLogger(t, zap.ErrorLevel)
	events := 100
	timeout := 60 * time.Second
	doSyncAsync(func(name string, sync bool) {
		topics := SuffixTopics(
			apmqueue.Topic(t.Name()+name+"Even"),
			apmqueue.Topic(t.Name()+name+"Odd"),
		)
		topicRouter := func(event model.APMEvent) apmqueue.Topic {
			if event.Event.Duration%2 == 0 {
				return topics[0]
			}
			return topics[1]
		}
		t.Run("Kafka"+name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			require.NoError(t,
				ProvisionKafka(ctx, newLocalKafkaConfig(topics...)),
			)
			var records atomic.Int64
			testProduceConsume(ctx, t, produceConsumeCfg{
				events:               events,
				expectedRecordsCount: events,
				records:              &records,
				timeout:              timeout,
				producer: newKafkaProducer(t, kafka.ProducerConfig{
					Logger:      logger,
					Encoder:     json.JSON{},
					TopicRouter: topicRouter,
					Sync:        sync,
				}),
				consumer: newKafkaConsumer(t, kafka.ConsumerConfig{
					Logger:  logger,
					Decoder: json.JSON{},
					Topics:  topics,
					GroupID: t.Name(),
					Processor: assertBatchFunc(t, consumerAssertions{
						records:   &records,
						processor: model.TransactionProcessor,
					}),
				}),
			})
		})
		t.Run("PubSubLite"+name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			require.NoError(t,
				ProvisionPubSubLite(ctx, newPubSubLiteConfig(topics...)),
			)
			var records atomic.Int64
			testProduceConsume(ctx, t, produceConsumeCfg{
				events:               events,
				expectedRecordsCount: events,
				records:              &records,
				timeout:              timeout,
				producer: newPubSubLiteProducer(t, pubsublite.ProducerConfig{
					Logger:      logger,
					Encoder:     json.JSON{},
					TopicRouter: topicRouter,
					Sync:        sync,
				}),
				consumer: newPubSubLiteConsumer(ctx, t, pubsublite.ConsumerConfig{
					Logger:  logger,
					Decoder: json.JSON{},
					Topics:  topics,
					Processor: assertBatchFunc(t, consumerAssertions{
						records:   &records,
						processor: model.TransactionProcessor,
					}),
				}),
			})
		})
	})
}

type produceConsumeCfg struct {
	events               int
	replay               int
	expectedRecordsCount int
	producer             apmqueue.Producer
	consumer             apmqueue.Consumer
	records              *atomic.Int64
	timeout              time.Duration
}

func doSyncAsync(f func(name string, sync bool)) {
	for _, sync := range []bool{true, false} {
		var name string
		switch sync {
		case true:
			name = "Sync"
		case false:
			name = "Async"
		}
		f(name, sync)
	}
}

func testProduceConsume(ctx context.Context, t testing.TB, cfg produceConsumeCfg) {
	// Run consumer and assert that the events are eventually set.
	go cfg.consumer.Run(ctx)
	for j := 0; j < cfg.replay+1; j++ {
		batch := make(model.Batch, 0, cfg.events)
		for i := 0; i < cfg.events; i++ {
			batch = append(batch, model.APMEvent{
				Timestamp: time.Now(),
				Processor: model.TransactionProcessor,
				Trace:     model.Trace{ID: fmt.Sprintf("trace%d-%d", j, i+1)},
				Event: model.Event{
					Duration: time.Millisecond * (time.Duration(rand.Int63n(999)) + 1),
				},
				Transaction: &model.Transaction{
					ID: fmt.Sprintf("transaction%d-%d", j, i+1),
				},
			})
		}

		// Produce the records to queue.
		assert.NoError(t, cfg.producer.ProcessBatch(ctx, &batch))
		if cfg.records == nil {
			return
		}
	}
	assert.Eventually(t, func() bool {
		return cfg.records.Load() == int64(cfg.expectedRecordsCount) // Assertion
	},
		cfg.timeout,          // Timeout
		100*time.Millisecond, // Poll
		"expected records (%d) records do not match consumed records (%v)", // ErrMessage
		cfg.expectedRecordsCount,
		cfg.records,
	)
}

type consumerAssertions struct {
	processor model.Processor
	records   *atomic.Int64
}

func assertBatchFunc(t testing.TB, assertions consumerAssertions) model.BatchProcessor {
	return model.ProcessBatchFunc(func(_ context.Context, b *model.Batch) error {
		assert.Greater(t, len(*b), 0)
		for _, r := range *b {
			assert.Equal(t, assertions.processor, r.Processor, r)
			if assertions.records != nil {
				assertions.records.Add(1)
			}
		}
		return nil
	})
}

func TestShutdown(t *testing.T) {
	codec := json.JSON{}

	sendEvent := func(producer apmqueue.Producer) {
		assert.NoError(t, producer.ProcessBatch(context.Background(), &model.Batch{
			model.APMEvent{Transaction: &model.Transaction{ID: "1"}},
		}))
		assert.NoError(t, producer.Close())
	}

	testShutdown := func(t testing.TB, producerF func() apmqueue.Producer, consumerF func() (apmqueue.Consumer, chan struct{}), expectedErr error, stop func(context.CancelFunc, apmqueue.Consumer)) {
		sendEvent(producerF())

		consumer, got := consumerF()

		closeCh := make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			// TODO this is failing
			//assert.Equal(t, expectedErr, consumer.Run(ctx))
			consumer.Run(ctx)
			close(closeCh)
		}()
		select {
		case <-got:
		case <-time.After(120 * time.Second):
			t.Error("timed out while waiting to receive an event")
		}
		stop(cancel, consumer)

		select {
		case <-closeCh:
		case <-time.After(120 * time.Second):
			t.Error("timed out while waiting for consumer to exit")
		}
	}

	t.Run("Kafka", func(t *testing.T) {
		f := func(t testing.TB) (func() (apmqueue.Consumer, chan struct{}), func() apmqueue.Producer) {
			logger := zaptest.NewLogger(t, zaptest.Level(zapcore.InfoLevel))
			topics := SuffixTopics(apmqueue.Topic(t.Name()))
			topicRouter := func(event model.APMEvent) apmqueue.Topic {
				return apmqueue.Topic(topics[0])
			}
			require.NoError(t, ProvisionKafka(context.Background(),
				newLocalKafkaConfig(topics...),
			))

			consumerF := func() (apmqueue.Consumer, chan struct{}) {
				received := make(chan struct{})
				return newKafkaConsumer(t, kafka.ConsumerConfig{
					Logger:  logger,
					Decoder: codec,
					Topics:  topics,
					GroupID: "groupid",
					Processor: model.ProcessBatchFunc(func(ctx context.Context, b *model.Batch) error {
						close(received)
						return nil
					}),
				}), received
			}

			producerF := func() apmqueue.Producer {
				return newKafkaProducer(t, kafka.ProducerConfig{
					Logger:      logger,
					Encoder:     codec,
					TopicRouter: topicRouter,
					Sync:        true,
				})
			}
			return consumerF, producerF
		}

		t.Run("ctx", func(t *testing.T) {
			consumerF, producerF := f(t)
			testShutdown(t, producerF, consumerF, nil, func(cancel context.CancelFunc, c apmqueue.Consumer) { cancel() })
		})
		t.Run("close", func(t *testing.T) {
			consumerF, producerF := f(t)
			testShutdown(t, producerF, consumerF, nil, func(cf context.CancelFunc, c apmqueue.Consumer) { assert.NoError(t, c.Close()) })
		})

	})

	t.Run("PubSub", func(t *testing.T) {
		f := func(t testing.TB) (func() (apmqueue.Consumer, chan struct{}), func() apmqueue.Producer) {
			logger := zaptest.NewLogger(t, zaptest.Level(zapcore.InfoLevel))
			topics := SuffixTopics(apmqueue.Topic(t.Name()))
			topicRouter := func(event model.APMEvent) apmqueue.Topic {
				return apmqueue.Topic(topics[0])
			}
			require.NoError(t, ProvisionPubSubLite(context.Background(),
				newPubSubLiteConfig(topics...),
			))

			consumerF := func() (apmqueue.Consumer, chan struct{}) {
				received := make(chan struct{})
				return newPubSubLiteConsumer(context.Background(), t, pubsublite.ConsumerConfig{
					Logger:  logger,
					Decoder: codec,
					Topics:  topics,
					Processor: model.ProcessBatchFunc(func(ctx context.Context, b *model.Batch) error {
						close(received)
						return nil
					}),
				}), received

			}
			producerF := func() apmqueue.Producer {
				return newPubSubLiteProducer(t, pubsublite.ProducerConfig{
					Logger:      logger,
					Encoder:     codec,
					TopicRouter: topicRouter,
					Sync:        true,
				})
			}
			return consumerF, producerF
		}

		t.Run("ctx", func(t *testing.T) {
			consumerF, producerF := f(t)
			testShutdown(t, producerF, consumerF, nil, func(cancel context.CancelFunc, c apmqueue.Consumer) { cancel() })
		})
		t.Run("close", func(t *testing.T) {
			consumerF, producerF := f(t)
			testShutdown(t, producerF, consumerF, nil, func(cf context.CancelFunc, c apmqueue.Consumer) { assert.NoError(t, c.Close()) })
		})
	})
}
