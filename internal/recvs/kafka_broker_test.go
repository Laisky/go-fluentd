package recvs

import (
 "context"
 "fmt"
 "os"
 "strings"
 "sync"
 "testing"
 "time"

 "gofluentd/internal/senders"
 "gofluentd/library"
 "github.com/IBM/sarama"
)

// This test is opt-in locally; the dependency compatibility workflow supplies a
// disposable Kafka broker. No credentials or existing topics are required.
type brokerUpgradeGroup struct {
 sarama.ConsumerGroup
 ready chan int32
 closed chan struct{}
 once sync.Once
}
func (g *brokerUpgradeGroup) Consume(ctx context.Context, topics []string, h sarama.ConsumerGroupHandler) error {
 return g.ConsumerGroup.Consume(ctx,topics,&brokerUpgradeHandler{ConsumerGroupHandler:h,ready:g.ready})
}
func (g *brokerUpgradeGroup) Close() error {
 err:=g.ConsumerGroup.Close()
 g.once.Do(func(){close(g.closed)})
 return err
}
type brokerUpgradeHandler struct {sarama.ConsumerGroupHandler;ready chan int32}
func (h *brokerUpgradeHandler) ConsumeClaim(s sarama.ConsumerGroupSession,c sarama.ConsumerGroupClaim)error{
 // InitialOffset is resolved before this hook: a Setup notification alone
 // could publish too early and race the OffsetNewest starting-position lookup.
 select {case h.ready<-c.Partition():case <-s.Context().Done():return nil}
 return h.ConsumerGroupHandler.ConsumeClaim(s,c)
}

func TestRegressionKafkaUpgradeRealBroker(t *testing.T) {
 address:=os.Getenv("FLUENTD_KAFKA_TEST_BROKERS")
 if address==""{t.Skip("set FLUENTD_KAFKA_TEST_BROKERS to run the real-broker contract")}
 brokers:=strings.Split(address,",")
 cfg:=sarama.NewConfig()
 admin,err:=sarama.NewClusterAdmin(brokers,cfg)
 if err!=nil{t.Fatal(err)}
 defer admin.Close()
 topic:=fmt.Sprintf("fluentd-upgrade-%d",time.Now().UnixNano())
 if err:=admin.CreateTopic(topic,&sarama.TopicDetail{NumPartitions:4,ReplicationFactor:1},false);err!=nil{t.Fatal(err)}
 defer admin.DeleteTopic(topic)
 producer,err:=senders.NewKafkaProducer(brokers)
 if err!=nil{t.Fatal(err)}
 defer producer.Close()

 send:=func(batch string,n int)map[int32]int64{
  records:=make([]*sarama.ProducerMessage,0,n+4)
  for i:=0;i<n;i++{records=append(records,&sarama.ProducerMessage{Topic:topic,Value:sarama.StringEncoder(fmt.Sprintf(`{"id":"%s-%d"}`,batch,i))})}
  for i:=0;i<4;i++{records=append(records,&sarama.ProducerMessage{Topic:topic,Value:sarama.StringEncoder("null")})}
  if err:=producer.SendMessages(records);err!=nil{t.Fatal(err)}
  want:=map[int32]int64{}
  for _,m:=range records{if m.Offset+1>want[m.Partition]{want[m.Partition]=m.Offset+1}}
  return want
 }
 // A newly created group must NOT replay records that existed before joining.
 send("historical",16)
 groupID:=topic+"-group"
 for round:=1;round<=2;round++{
  func(){
   ctx,cancel:=context.WithTimeout(context.Background(),60*time.Second)
   defer cancel()
   ready:=make(chan int32,16)
   closed:=make(chan struct{})
   r:=upgradeKafkaRecv()
   r.Brokers,r.Topics,r.Group=brokers,[]string{topic},groupID
   r.IsJSONFormat,r.TagKey,r.RewriteTag=true,"tag","delivered"
   r.IntervalDuration=50*time.Millisecond
   r.newConsumerGroup=func(b []string,g string,c *sarama.Config)(kafkaConsumerGroup,error){
    client,err:=sarama.NewConsumerGroup(b,g,c)
    if err!=nil{return nil,err}
    return &brokerUpgradeGroup{ConsumerGroup:client,ready:ready,closed:closed},nil
   }
   out:=make(chan *library.FluentMsg,128)
   r.SetSyncOutChan(out)
   done:=make(chan struct{})
   go func(){r.runConsumer(ctx);close(done)}()
   defer func(){cancel();select{case <-done:case <-time.After(15*time.Second):t.Error("consumer worker leaked after cancellation")}}()
   partitions:=map[int32]bool{}
   for len(partitions)<4{select{case p:=<-ready:partitions[p]=true;case <-ctx.Done():t.Fatal("consumer did not acquire all partitions")}}
   batch:=fmt.Sprintf("round%d",round)
   wantOffsets:=send(batch,64)
   seen:=map[string]bool{}
   ids:=map[int64]bool{}
   for len(seen)<64{
    select{
    case msg:=<-out:
     id,ok:=msg.Message["id"].(string)
     if !ok || !strings.HasPrefix(id,batch+"-"){t.Fatalf("historical, malformed or wrong-round record replayed: %+v",msg.Message)}
     if seen[id]||ids[msg.ID]{t.Fatalf("duplicate delivery: %s",id)}
     if msg.Tag!="delivered"||msg.Message["tag"]!="logs"{t.Fatal("tag rewrite contract changed")}
     seen[id],ids[msg.ID]=true,true
    case <-ctx.Done():t.Fatalf("received %d/64 records",len(seen))
    }
   }
   ticker:=time.NewTicker(50*time.Millisecond)
   defer ticker.Stop()
   for {
    offsets,err:=admin.ListConsumerGroupOffsets(groupID,map[string][]int32{topic:{0,1,2,3}})
    complete:=err==nil
    if complete{for partition,want:=range wantOffsets{block:=offsets.GetBlock(topic,partition);if block==nil||block.Err!=sarama.ErrNoError||block.Offset!=want{complete=false;break}}}
    if complete{break}
    select{case <-ticker.C:case <-ctx.Done():t.Fatalf("offsets did not advance to delivered/discarded frontier: %v",err)}
   }
   select{case extra:=<-out:t.Fatalf("unexpected extra delivery: %+v",extra.Message);default:}
   cancel()
   select{case <-closed:case <-time.After(15*time.Second):t.Fatal("consumer client did not close")}
   // The next iteration reuses groupID and must start from committed offsets.
  }()
 }
}
