# aero-vault — Python SDK

A zero-dependency Python client for the **aero-vault** AI-native file platform.
Built entirely on the standard library, so you can either `pip install` it or
copy the single `aero_vault.py` module into your project.

## Install

```bash
# from the repo
pip install ./sdk/python

# or just drop the file next to your code
cp sdk/python/aero_vault.py your_project/
```

## Usage

```python
from aero_vault import Client

av = Client("http://localhost:8080", token="prod-rw", tenant="acme")

# --- files ---
av.upload("docs/readme.txt", b"hello world", content_type="text/plain")
data = av.get("docs/readme.txt")              # -> b"hello world"
av.download("docs/readme.txt", "./readme.txt")
meta = av.stat("docs/readme.txt")             # -> Object(size=..., etag=...)
for obj in av.iter_objects(prefix="docs/"):   # auto-paginates
    print(obj.key, obj.size)
av.delete("docs/readme.txt")                  # soft delete; hard=True wipes bytes

# presigned URLs, tags, versions, object lock
url = av.presign("docs/readme.txt", op="get", expires=900)
av.put_tags("docs/readme.txt", {"team": "research"})
av.list_versions("docs/readme.txt")

# --- AI ---
hits = av.search("vector database", k=5, mode="hybrid")
for h in hits:
    print(h.score, h.object_key, h.chunk[:60])

reply = av.chat("what is in the docs?")
print(reply.answer)
for c in reply.citations:
    print(" -", c.object_key)

# streaming tokens (SSE)
for token in av.chat_stream("summarize everything"):
    print(token, end="", flush=True)

# tool-calling agent
print(av.agent("find the largest file and tell me its name")["answer"])
```

## Authentication & tenancy

| Concern   | How                                                              |
|-----------|-----------------------------------------------------------------|
| API key   | `Client(..., token="<key>")` → `Authorization: Bearer <key>`    |
| `X-Api-Key` style | `Client(..., token="<key>", api_key_header=True)`       |
| JWT       | same as API key — pass the JWT as `token`                       |
| Tenant    | `Client(..., tenant="acme")` → `X-Aero-Tenant: acme`            |
| Env vars  | `AERO_VAULT_URL`, `AERO_VAULT_TOKEN`, `AERO_VAULT_TENANT`        |

## Errors

Non-2xx responses raise `AeroVaultError` with `.status`, `.code`, `.message`,
and `.request_id` populated from the platform's error envelope.

```python
from aero_vault import AeroVaultError

try:
    av.get("missing.txt")
except AeroVaultError as e:
    if e.status == 404:
        ...
```

## CLI

The module doubles as a smoke-test CLI (also installed as `aero-vault`):

```bash
python aero_vault.py --url http://localhost:8080 --token prod-rw ping
python aero_vault.py put docs/a.txt ./a.txt
python aero_vault.py ls docs/
python aero_vault.py search "hello" -k 5 --mode hybrid
python aero_vault.py chat "summarize the docs" --stream
```
