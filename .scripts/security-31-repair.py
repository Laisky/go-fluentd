from pathlib import Path
import hashlib
p=Path('/tmp/evidence31/transport.b64')
s=p.read_text().strip()
assert hashlib.sha256(s.encode()).hexdigest()=='1f611fd17c74595021600971d08c6950acac858acfcb861bf96b6b56dee3a094'
for a,b,value in reversed([(325, 325, 'm'), (671, 671, 'K'), (673, 674, 'H'), (1219, 1219, 'qkLYLBrDZrF4BOB7x6xB2cwYk/HInFQ'), (3107, 3108, '')]):
    s=s[:a]+value+s[b:]
assert hashlib.sha256(s.encode()).hexdigest()=='acd00f8a55e991f1d1c3af346511e18075042187b80830456887cd954e69c4d8'
p.write_text(s+'\n')
