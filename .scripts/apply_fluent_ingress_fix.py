#!/usr/bin/env python3
"""Apply minimal, guarded integration changes to the reviewed receiver."""
from pathlib import Path

def replace_once(text: str, before: str, after: str) -> str:
    if text.count(before) != 1:
        raise RuntimeError(f"expected exactly one occurrence of {before!r}")
    return text.replace(before, after, 1)

path = Path("internal/recvs/fluentd.go")
s = path.read_text()
s = replace_once(s, 'import (\n\t"bytes"', 'import (\n\t"bufio"\n\t"bytes"')
s = replace_once(s, '\n\t"github.com/tinylib/msgp/msgp"', '')
s = replace_once(s, 'type FluentdRecvCfg struct {\n', 'type FluentdRecvCfg struct {\n\tIngress FluentIngressCfg\n')
s = replace_once(s, 'func (r *FluentdRecv) valid() error {\n', 'func (r *FluentdRecv) valid() error {\n\tif err := r.Ingress.setDefaultsAndValidate(); err != nil {\n\t\treturn err\n\t}\n')
s = replace_once(s, '\tvar connections sync.WaitGroup\n', '\tvar connections sync.WaitGroup\n\tslots := make(chan struct{}, r.Ingress.MaxConnections)\n')
s = replace_once(s, '''\t\tfor {
\t\t\tconn, err := ln.Accept()
\t\t\tif err != nil {
\t\t\t\tbreak
\t\t\t}
\t\t\tconnections.Add(1)
\t\t\tgo func(conn net.Conn) { defer connections.Done(); r.decodeMsg(ctx, conn) }(conn)
\t\t}''', '\t\tr.acceptFluent(ctx, ln, slots, &connections)')
s = replace_once(s, 'reader = msgp.NewReader(conn)', 'reader = bufio.NewReader(conn)')
s = replace_once(s, 'reader2 *msgp.Reader', 'reader2 *bufio.Reader')
s = replace_once(s, 'eof     = msgp.WrapError(io.EOF)', 'eof     = io.EOF')
s = replace_once(s, '\t\tif err = v.DecodeMsg(reader); err == eof {', '\t\tclear(v2)\n\t\t// Do not retain the preceding packed payload during the next idle wait.\n\t\tif buf2 != nil { buf2.Reset(nil) }\n\t\tif reader2 != nil { reader2.Reset(buf2) }\n\t\tbudget, decodeErr := r.readFluentFrame(conn, reader, &v)\n\t\tif err = decodeErr; err == eof {')
s = replace_once(s, 'reader2 = msgp.NewReader(buf2)', 'reader2 = bufio.NewReader(buf2)')
s = replace_once(s, 'if err = v2.DecodeMsg(reader2); err == eof {', 'if err = decodeFluentFrame(reader2, budget, &v2, 2, 2); err == eof {')
s = replace_once(s, '''\t\t\t\t} else if err != nil {
\t\t\t\t\tr.logger.Warn("discard msg since unknown message format, cannot decode")
\t\t\t\t\tbreak''', '''\t\t\t\t} else if err != nil {
\t\t\t\t\tr.logger.Warn("reject malformed or oversized packed Fluent frame", zap.Error(err))
\t\t\t\t\tif isFluentIngressLimit(err) { return }
\t\t\t\t\t// The bounded binary payload is consumed: the next outer boundary is known.
\t\t\t\t\tbreak''')
path.write_text(s)
path = Path("internal/controller/controllor.go")
s = path.read_text()
s = replace_once(s, 'recvs.NewFluentdRecv(&recvs.FluentdRecvCfg{\n', 'recvs.NewFluentdRecv(&recvs.FluentdRecvCfg{\n\t\t\t\t\tIngress:                fluentIngressConfig(name),\n')
path.write_text(s)
