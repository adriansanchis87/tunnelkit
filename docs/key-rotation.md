# Key rotation

There are three secrets: each client's SSH key, the server's host key and the
optional UDP heartbeat key.

## Client key

The server identifies a client by its key; two keys can be valid at the same time,
so a rotation needs no downtime:

1. Generate the new key (`ssh-keygen -t ed25519 -N "" -f id_new -C tk-site1-web`,
   same comment/name as before).
2. Add its line to `data/authorized_keys`, with the same `permitlisten` list. Keep
   the old line for now.
3. Switch the client to the new key file and restart it. It connects with the new
   key; a connection with the same name replaces the old session.
4. Check the panel shows the client connected.
5. Delete the old line. As explained in
   [Operations](operations.md#adding-and-revoking-clients), an already open session
   survives; if the old key may be compromised, restart the server after deleting it.

## Server host key

The host key is the server's identity, so it should change only if it may have
been exposed. A server has **one** host key at a time: there is no overlap in
which both the old and the new key are accepted, so each client has a short outage
between the server change and its own update.

1. Make sure you can reach every client by another path, because the tunnel is
   what you would normally use. Note that updating a pinned client's `host_key`
   requires touching the client itself.
2. Stop the server, move `data/host_key` away (keep it until the end) and start the
   server: it creates a new key and logs the new `TK_HOST_KEY` value.
3. Set the new value as `host_key` on each client and restart it. Clients that were
   not pinned and keep a `known_hosts` file will also refuse the changed key until
   that entry is removed.
4. Securely delete the old key and its backups once all clients are back.

If you only lost the server and have the `host_key` backup, restore the backup
instead: clients see no change at all.

## UDP heartbeat key

`TK_UDP_KEY` is shared by the server and every client that sends the heartbeat.
Packets with a wrong signature get no reply, so after changing it on the server a
client with the old key simply shows no UDP heartbeat until you update it. The
heartbeat is a diagnostic only; the tunnel itself is unaffected, so you can change
the key on the server and then on the clients at your own pace.
