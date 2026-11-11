# Google Compute Engine

Google's free tier includes one e2-micro VM with a 30 GB standard disk, and
that is enough for a whole Stratus: the image, the SQLite database and the
blobs on the same disk. No button and no container platform. It is a VM
running Container-Optimized OS, with
[`deploy/gce/cloud-init.yaml`](../../deploy/gce/cloud-init.yaml) as its
user-data and Caddy in front for TLS.

Cloud Run is not an option. It scales to zero, which stops the indexer and
the sweep, it has no disk, and it cuts an HTTP/1 request body off at 32 MiB,
so a video would never finish uploading.

## Before you start

You need a project with billing enabled, because the free tier still asks for
a card. You also need a domain name you can point at the VM, since Caddy needs
one to get a certificate.

## Creating it

The free tier covers `us-west1`, `us-central1` and `us-east1` only. In any
other region the same VM is billed.

```sh
curl -fsSLO https://raw.githubusercontent.com/stratus-private-cloud/stratus-backend/main/deploy/gce/cloud-init.yaml

gcloud compute firewall-rules create stratus-web \
  --allow tcp:80,tcp:443,udp:443 --target-tags stratus

gcloud compute addresses create stratus --region us-central1
gcloud compute addresses describe stratus --region us-central1 --format 'value(address)'
```

Point the domain's `A` record at that address, then:

```sh
gcloud compute instances create stratus \
  --zone us-central1-a --machine-type e2-micro \
  --image-family cos-stable --image-project cos-cloud \
  --boot-disk-size 30GB --boot-disk-type pd-standard --no-boot-disk-auto-delete \
  --address stratus --tags stratus \
  --metadata-from-file user-data=cloud-init.yaml \
  --metadata stratus-username=you,stratus-password='choose one',stratus-domain=cloud.example.com
```

Within a couple of minutes `https://cloud.example.com/` serves the sign-in
page. If the DNS record has not propagated yet, Caddy keeps retrying the
certificate on its own.

`--metadata` splits on commas, so a password that contains one has to come
from a file: `--metadata-from-file stratus-password=pw.txt`. The values are
instance metadata, so anybody with read access to the project can read them.

## What it costs

| What | Free tier | Past it |
|---|---|---|
| e2-micro, one per billing account | 730 hours a month | ~$7 a month |
| Standard persistent disk | 30 GB | ~$0.04 per GB |
| Outbound traffic | **1 GB a month** | ~$0.12 per GB |
| Static external IPv4 address | unclear | ~$3.65 a month |

**Outbound traffic is the limit you will reach first.** Uploading is inbound
and free, but every photo you look at, track you stream and file you sync
back counts against the 1 GB, and a single film is more than that. A backup
that is rarely read fits easily. A music library played from it every day
does not.

Google's own pages disagree on whether the IPv4 address is part of the free
tier, so expect it on the bill until somebody has seen a real one. The address
has to be static: an ephemeral one changes when the VM stops, and the DNS
record stops pointing at it. A Google load balancer would remove the need for
Caddy but costs about $18 a month, more than everything else in this table.

## Things worth knowing

- **The library lives on the boot disk**, in
  `/mnt/stateful_partition/stratus`, the part of a COS disk that survives a
  reboot and an OS update. `--no-boot-disk-auto-delete` is what keeps it when
  the VM is deleted. Without that flag, deleting the VM deletes the library
  with it.
- **1 GB of RAM is plenty for the server, and ffmpeg is slow on it.** The
  demo runs in 256 MB. The e2-micro only gets a quarter of a CPU between
  bursts, so the first pass of thumbnails over a camera roll takes its time;
  `/status` shows how far it has got.
- **One VM.** Never start a second one against the same disk.
- **Upgrading is a restart**, which pulls `:main` again:
  `gcloud compute ssh stratus -- sudo systemctl restart stratus`.
- **Changing a value** is `gcloud compute instances add-metadata stratus
  --metadata stratus-password=…` followed by
  `sudo systemctl restart stratus-setup stratus` (plus `caddy` for the
  domain). Changing the password signs out every browser and client.
- **Not `create-with-container`.** The agent behind it was shut down on
  2026-07-31. COS itself is still supported, and cloud-init comes with it.
- Caddy's certificates live on the same disk, so a reboot does not request
  new ones and run into Let's Encrypt's weekly limit.

## Changing the options

Any variable in the README's configuration table goes in the
`ExecStart` of `stratus.service` in your copy of `cloud-init.yaml`, as
`-e NAME=value`. Then
`gcloud compute instances add-metadata stratus --metadata-from-file user-data=cloud-init.yaml`
and a reboot apply it, because COS writes the units again on every boot.
Google sells both ends of the seams, Cloud Storage over its S3-compatible API
and Cloud SQL for PostgreSQL, but neither is free, and `/data` stays either
way: it is also the scratch space the indexer and the thumbnail generator
work in.

## What a real deploy answered

Nothing yet. This has not been run on a real Google Cloud project. Locally,
the same two `docker run` lines behind Caddy answered `/readyz` with
`database: ok` and `storage: ok`, set a `Secure` cookie on sign-in, accepted a
WebDAV `PUT`, and passed through a password containing a space and a quote
unchanged. Still unproven: cloud-init on COS, the firewall, the certificate,
and the bill.
