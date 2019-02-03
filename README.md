# kubersync

Like rsync, but for Kubernetes secrets.

**Note:** In most cases you should probably prefer

## Usage


```
$ kubectl create secret generic my-secret
$ kubersync -secret my-secret -path /var/secrets/my-secret
$ echo "hunter2" > /var/secrets/my-secret/password
$ kubectl get secrets my-secret -o yaml
apiVersion: v1
data:
  password: aHVudGVyMgo=
kind: Secret
metadata:
  creationTimestamp: 2019-02-03T16:14:12Z
  name: my-secret
  namespace: default
  resourceVersion: "37676166"
  selfLink: /api/v1/namespaces/default/secrets/my-secret
  uid: be89a489-27ce-11e9-9460-42010a80007d
type: Opaque
```

Elsewhere:

```
$ kubersync -secret my-secret -path /var/secrets/my-secret
$ cat /var/secrets/my-secret/password
hunter2
```

## Bugs

This whole thing is super race-condition-y. If this matters for your use case, use something else.

Deleting files is tricky. We can't tell if missing files should be removed from the secret, or if they should be created locally. On the initial sync, we merge the files from  the secret and the local file system. After the initial sync, if you delete files or removing items from the secret, kubersync usually delete them.
