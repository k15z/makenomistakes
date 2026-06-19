# File Vault

`file-vault` exposes documents from a configured vault directory.

Run tests:

```sh
npm test
```

Run the service:

```sh
node src/server.js ./vault
```

Example request:

```sh
curl 'http://127.0.0.1:3000/documents?name=welcome.txt'
```
