# Third-party notices

rave-mate (AGPL-3.0-or-later) incorporates work derived from third-party projects.
Their licenses are reproduced below as required.

## serato-connect (MIT)

`internal/seratoremote` (the Serato Remote OSC-over-TCP protocol: framing, OSC codec,
mDNS advert, handshake driver, per-deck status parsing) and the `internal/serato` History
`adat` field ids are derived from **chrisle/serato-connect** - its `docs/protocol.md` wire
specification and `src/remote/*` implementation. The framing + OSC parity tests are ports
of that project's test vectors.

Source: https://github.com/chrisle/serato-connect

```
MIT License

Copyright (c) 2026 Chris Le

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```
