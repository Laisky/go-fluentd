package recvs

import (
 "bytes"
 "context"
 "net"
 "reflect"
 "runtime"
 "sync"
 "testing"
 "time"
 "gofluentd/library"
)

func ingressReceiver(t *testing.T,cfg FluentIngressCfg)*FluentdRecv{t.Helper();r:=newComponentFluent(t);r.Ingress=cfg;if err:=r.Ingress.setDefaultsAndValidate();err!=nil{t.Fatal(err)};r.concators=[]chan *library.FluentMsg{make(chan *library.FluentMsg,32)};return r}
func ingressPipe(t *testing.T,r *FluentdRecv)(net.Conn,<-chan struct{}){t.Helper();server,client:=net.Pipe();ctx,cancel:=context.WithCancel(context.Background());done:=make(chan struct{});go func(){defer close(done);r.decodeMsg(ctx,server)}();t.Cleanup(func(){cancel();client.Close();server.Close();<-done});if err:=client.SetDeadline(time.Now().Add(2*time.Second));err!=nil{t.Fatal(err)};return client,done}
func waitIngressClosed(t *testing.T,done <-chan struct{}){t.Helper();select{case <-done:case <-time.After(time.Second):t.Fatal("Fluent decoder did not close")}}

func TestFluentIngressRejectsNestedAndPackedHeaders(t *testing.T){
 for name,input:=range map[string][]byte{
  "max outer array":{0xdd,0xff,0xff,0xff,0xff},
  "array":{0x92,0xa0,0xdd,0xff,0xff,0xff,0xff},
  "map":{0x93,0xa0,0,0xdf,0xff,0xff,0xff,0xff},
  "string":{0x93,0xa0,0,0x81,0xa1,'x',0xdb,0xff,0xff,0xff,0xff},
  "binary":{0x92,0xa0,0xc6,0xff,0xff,0xff,0xff},
  "extension":{0x93,0xa0,0,0x81,0xa1,'x',0xc9,0xff,0xff,0xff,0xff},
  "packed array":{0x92,0xa0,0xc4,5,0xdd,0xff,0xff,0xff,0xff},
  "packed nested map":{0x92,0xa0,0xc4,7,0x92,0,0xdf,0xff,0xff,0xff,0xff},
 }{t.Run(name,func(t *testing.T){r:=ingressReceiver(t,FluentIngressCfg{});client,done:=ingressPipe(t,r);if _,err:=client.Write(input);err!=nil{t.Fatal(err)};waitIngressClosed(t,done);if len(r.concators[0])!=0{t.Fatal("rejected input published a record")}})}
}

func TestFluentIngressDeadlines(t *testing.T){
 for name,input:=range map[string][]byte{"idle":nil,"partial header":{0xdd},"partial body":{0x93,0xa1,'t',0,0x81,0xa1,'x',0xda,0,32}}{t.Run(name,func(t *testing.T){r:=ingressReceiver(t,FluentIngressCfg{IdleTimeout:40*time.Millisecond,FrameTimeout:80*time.Millisecond});client,done:=ingressPipe(t,r);if len(input)>0{if _,err:=client.Write(input);err!=nil{t.Fatal(err)}};waitIngressClosed(t,done)})}
}

func TestFluentIngressTrickleCannotExtendFrameDeadline(t *testing.T){
 r:=ingressReceiver(t,FluentIngressCfg{IdleTimeout:time.Second,FrameTimeout:100*time.Millisecond});client,done:=ingressPipe(t,r)
 if _,err:=client.Write([]byte{0x93,0xa1,'t',0,0x81,0xa1,'x',0xda,0,255});err!=nil{t.Fatal(err)}
 writesDone:=make(chan struct{});go func(){defer close(writesDone);ticker:=time.NewTicker(10*time.Millisecond);defer ticker.Stop();for{select{case <-done:return;case <-ticker.C:if _,err:=client.Write([]byte{'x'});err!=nil{return}}}}()
 waitIngressClosed(t,done);<-writesDone
}

func TestFluentIngressPersistentWireFormatsAndOwnership(t *testing.T){
 entry:=library.FluentBatchMsg{0,map[string]interface{}{"value":"packed"}};packed,err:=entry.MarshalMsg(nil);if err!=nil{t.Fatal(err)}
 frames:=[]library.FluentBatchMsg{{"logs",0,map[string]interface{}{"value":"message"}},{"logs",[]interface{}{[]interface{}{0,map[string]interface{}{"value":"forward"}}}},{"logs",packed,map[string]interface{}{}},{"logs",0,map[string]interface{}{"value":"options"},map[string]interface{}{}}}
 var wire []byte;for _,frame:=range frames{wire,err=frame.MarshalMsg(wire);if err!=nil{t.Fatal(err)}}
 r:=ingressReceiver(t,FluentIngressCfg{MaxFrameBytes:128,MaxValueBytes:128});client,done:=ingressPipe(t,r)
 wire=bytes.Repeat(wire,2);if len(wire)<=128{t.Fatal("fixture must exceed one frame budget")}
 if _,err=client.Write(wire);err!=nil{t.Fatal(err)};client.Close();waitIngressClosed(t,done)
 for i:=0;i<2;i++{for _,want:=range []string{"message","forward","packed","options"}{got:=recvTake(t,r.concators[0]);if got.Tag!="logs"||got.Message["value"]!=want||len(got.ExtIds)!=0{t.Fatalf("corrupted decoded record: %+v",got)}}}
}

func TestFluentIngressConnectionAdmissionAndSlotRecovery(t *testing.T){
 r:=ingressReceiver(t,FluentIngressCfg{MaxConnections:2,IdleTimeout:10*time.Second});ln,err:=net.Listen("tcp","127.0.0.1:0");if err!=nil{t.Fatal(err)}
 ctx,cancel:=context.WithCancel(context.Background());slots:=make(chan struct{},r.Ingress.MaxConnections);var workers sync.WaitGroup;acceptedDone:=make(chan struct{})
 go func(){defer close(acceptedDone);r.acceptFluent(ctx,ln,slots,&workers)}();t.Cleanup(func(){cancel();ln.Close();<-acceptedDone;workers.Wait()})
 dial:=func()net.Conn{c,err:=net.DialTimeout("tcp",ln.Addr().String(),time.Second);if err!=nil{t.Fatal(err)};t.Cleanup(func(){c.Close()});return c}
 waitSlots:=func(want int){deadline:=time.Now().Add(time.Second);for len(slots)!=want&&time.Now().Before(deadline){time.Sleep(time.Millisecond)};if len(slots)!=want{t.Fatalf("slots=%d want=%d",len(slots),want)}}
 first:=dial();waitSlots(1);_=dial();waitSlots(2);baseline:=runtime.NumGoroutine()
 for i:=0;i<64;i++{c:=dial();if err:=c.SetReadDeadline(time.Now().Add(time.Second));err!=nil{t.Fatal(err)};var one [1]byte;if _,err:=c.Read(one[:]);err==nil{t.Fatal("overloaded connection remained usable")}else if e,ok:=err.(net.Error);ok&&e.Timeout(){t.Fatal("overloaded connection was not promptly rejected")};c.Close()}
 if len(slots)!=2{t.Fatal("overload changed admitted worker count")};if delta:=runtime.NumGoroutine()-baseline;delta>8{t.Fatalf("goroutines grew after rejection: %d",delta)}
 first.Close();waitSlots(1);c:=dial();waitSlots(2)
 if _,err:=c.Write([]byte{0xdd,0xff,0xff,0xff,0xff});err!=nil{t.Fatal(err)};waitSlots(1)
 cancel();ln.Close();<-acceptedDone;workers.Wait();if len(slots)!=0{t.Fatal("shutdown leaked a connection slot")}
}

func TestFluentIngressConfigDefaultsAndNegativeValues(t *testing.T){
 var cfg FluentIngressCfg;if err:=cfg.setDefaultsAndValidate();err!=nil{t.Fatal(err)}
 if cfg.MaxConnections!=32||cfg.IdleTimeout!=30*time.Second||cfg.FrameTimeout!=10*time.Second||cfg.MaxFrameBytes!=8<<20{t.Fatalf("defaults: %+v",cfg)}
 for i:=0;i<reflect.TypeOf(cfg).NumField();i++{bad:=FluentIngressCfg{};reflect.ValueOf(&bad).Elem().Field(i).SetInt(-1);if err:=bad.setDefaultsAndValidate();err==nil{t.Fatalf("negative field %d accepted",i)}}
}
