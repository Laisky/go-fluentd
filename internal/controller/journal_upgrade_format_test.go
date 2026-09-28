package controller

import (
 "context"
 "fmt"
 "os"
 "path/filepath"
 "reflect"
 "runtime"
 "strings"
 "testing"

 "gofluentd/library"
 journal "github.com/Laisky/go-journal"
)

func upgradeFormatPayload(id int64) map[string]interface{} {
 p := upgradePayload(id)
 // Cross the decoder's ordinary buffer and the writer's 4 MiB staging prefix.
 if id == 5 { p["large"] = strings.Repeat("x", (4<<20)+17) }
 if id == 7 { p["large"] = strings.Repeat("z", (128<<10)+7) }
 p["binary"] = []byte{0, byte(id), 255}
 return p
}

func seedUpgradeFormat(t *testing.T, dir string, compressed bool) {
 t.Helper()
 backend := upgradeBackend(t, dir, compressed)
 for id:=int64(1);id<=12;id++ {
  if err:=backend.WriteData(&journal.Data{ID:id, Data:map[string]interface{}{"tag":"source", "message":upgradeFormatPayload(id)}});err!=nil{t.Fatal(err)}
  if id%3==0 {if err:=backend.WriteId(id);err!=nil{t.Fatal(err)}}
  if id%4==0 {
   if err:=backend.Sync();err!=nil{t.Fatal(err)}
   if err:=backend.Rotate(context.Background());err!=nil{t.Fatal(err)}
  }
 }
 if err:=backend.Sync();err!=nil{t.Fatal(err)}
 backend.Close()
}

func checkUpgradeFormat(t *testing.T, dir string, compressed bool) {
 t.Helper()
 backend:=upgradeBackend(t,dir,compressed)
 controller:=upgradeController(backend,64)
 high,err:=controller.LoadMaxID()
 if err!=nil || high!=12 {t.Fatalf("ACK frontier=%d err=%v, want 12",high,err)}
 out:=make(chan *library.FluentMsg,16)
 if _,err:=controller.ProcessLegacyMsg(out);err!=nil{t.Fatal(err)}
 close(out)
 got:=map[int64]map[string]interface{}{}
 for msg:=range out {
  if msg.Tag!="source"||msg.JournalTag!="source"{t.Fatalf("replay owner changed: id=%d",msg.ID)}
  if _,ok:=got[msg.ID];ok{t.Fatalf("duplicate replay id=%d",msg.ID)}
  got[msg.ID]=msg.Message
 }
 // Compare only after all segments were decoded and temporary readers released.
 // This catches payload aliases into a subsequently reused decoder buffer.
 runtime.GC()
 want:=map[int64]map[string]interface{}{}
 for id:=int64(1);id<=12;id++{if id%3!=0{want[id]=upgradeFormatPayload(id)}}
 if !reflect.DeepEqual(got,want){t.Fatal("replayed payloads were lost, ACKed, duplicated or changed after decoder reuse")}
 backend.Close()
}

func TestRegressionJournalUpgradeRetainedPayloadAcrossSegments(t *testing.T) {
 for _,compressed:=range []bool{false,true}{t.Run(fmt.Sprint(compressed),func(t *testing.T){
  dir:=t.TempDir()
  seedUpgradeFormat(t,dir,compressed)
  for restart:=0;restart<2;restart++{checkUpgradeFormat(t,dir,compressed)}
 })}
}

// The compatibility workflow runs this same public consumer contract in two
// separate processes: seed with the previous pinned journal, check with HEAD.
// An ordinary unit-test run does not create or consume an external fixture.
func TestRegressionJournalUpgradeDiskFixture(t *testing.T) {
 mode:=os.Getenv("FLUENTD_JOURNAL_UPGRADE_MODE")
 if mode==""{t.Skip("cross-version driver supplies seed/check mode")}
 root:=os.Getenv("FLUENTD_JOURNAL_UPGRADE_DIR")
 if root==""{t.Fatal("cross-version fixture directory is required")}
 for _,compressed:=range []bool{false,true}{t.Run(fmt.Sprint(compressed),func(t *testing.T){
  dir:=filepath.Join(root,fmt.Sprintf("gzip-%v",compressed))
  switch mode {
  case "seed":
   if _,err:=os.Stat(dir);!os.IsNotExist(err){t.Fatal("seed requires a fresh directory")}
   if err:=os.MkdirAll(dir,0700);err!=nil{t.Fatal(err)}
   seedUpgradeFormat(t,dir,compressed)
  case "check":
   if _,err:=os.Stat(dir);err!=nil{t.Fatal(err)}
   for restart:=0;restart<2;restart++{checkUpgradeFormat(t,dir,compressed)}
  default:t.Fatalf("unknown fixture mode %q",mode)
  }
 })}
}
