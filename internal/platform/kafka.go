package platform

import (
 "context"
 "encoding/json"
 "github.com/segmentio/kafka-go"
)

type Event struct { ID string `json:"id"`; Type string `json:"type"`; OccurredAt string `json:"occurred_at"`; Data any `json:"data"` }
type Producer interface { Publish(context.Context,string,string,Event) error }
type KafkaProducer struct { Writer *kafka.Writer }
func (p *KafkaProducer) Publish(ctx context.Context,topic,key string,e Event) error { b,err:=json.Marshal(e);if err!=nil{return err};return p.Writer.WriteMessages(ctx,kafka.Message{Topic:topic,Key:[]byte(key),Value:b}) }
type Consumer struct { Reader *kafka.Reader; Handler func(context.Context,Event) error }
func (c *Consumer) Run(ctx context.Context) error { for {m,e:=c.Reader.FetchMessage(ctx);if e!=nil{return e};var ev Event;if e=json.Unmarshal(m.Value,&ev);e==nil {e=c.Handler(ctx,ev)};if e!=nil{continue};if e=c.Reader.CommitMessages(ctx,m);e!=nil{return e} } }
