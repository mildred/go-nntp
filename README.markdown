# go nntp

I needed a way to gate some web services into traditional readers.  I
wrote an NNTP client and server.

I'm still working on coming up with the exact right interfaces, but
take a look at [the couchserver][couchserver] example to see what it
takes to build a custom NNTP server with your own backend.

## Reference

- [RFC977]
- [RFC3977]

[couchserver]: examples/couchserver/couchserver.go
[RFC977]: https://tools.ietf.org/html/rfc977
[RFC3977]: https://tools.ietf.org/html/rfc3977

