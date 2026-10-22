# Third-party notices

immich-amazon-sync is licensed AGPL-3.0-only; see LICENSE. Its Amazon Photos
adapter uses the public aphotos SDK from github.com/thedavidweng/amazon-photos,
Copyright (c) 2026 David Weng, under the MIT license below. The exact SDK revision
is pinned in go.mod; no Rainforest private code is imported. Rainforest and the
SDK are separate projects by the same author. The upstream sync projects informed
requirements; their implementations were not copied.

Other Go dependency versions are recorded in go.mod/go.sum. Cobra and pflag are
Apache-2.0; modernc SQLite and its transitive dependencies retain their upstream
licenses. Alpine packages retain the licenses distributed with the base image.

The local dashboard embeds the Latin subset of Cal Sans
(`internal/admin/calsans-latin.woff2`), Copyright 2022 The Cal Sans Project
Authors (https://github.com/calcom/font), licensed under the SIL Open Font
License 1.1 (https://openfontlicense.org).

## aphotos SDK: full MIT license

MIT License

Copyright (c) 2026 David Weng

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
