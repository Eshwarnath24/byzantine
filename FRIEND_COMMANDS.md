# Commands for Friend's Laptop (10.12.227.66)

## Option 1: UDP Auto-Discovery (Recommended)
Just run these commands on your friend's laptop. Nodes will auto-discover within 2-5 seconds:

```powershell
.\hotstuff.exe 3
.\hotstuff.exe 4
```

**In new terminals**, run these to check the logs and see when they discover Nodes 1 & 2.

---

## Option 2: Explicit IP Connection (If UDP fails)

If auto-discovery doesn't work after 10 seconds, use **explicit IP** commands:

**YOUR IP**: 10.12.226.231

Friend should run:
```powershell
.\hotstuff.exe 3 1=10.12.226.231 2=10.12.226.231
.\hotstuff.exe 4 1=10.12.226.231 2=10.12.226.231
```

Node 3 and 4 will be told exactly where to find Nodes 1 and 2.

---

## Pre-Flight Checklist

- [ ] Nodes 1 & 2 are running on your laptop (10.12.226.231)
- [ ] Friend's laptop is 10.12.227.66
- [ ] Both laptops are on **same WiFi network**
- [ ] Friend has run `go build .` before starting nodes

---

## Firewall Rule (if needed)

If explicit IPs don't work either, friend should open firewall:

```powershell
netsh advfirewall firewall add rule name="HotStuff-Consensus" dir=in action=allow protocol=TCP localport=7001-7004
netsh advfirewall firewall add rule name="HotStuff-Web" dir=in action=allow protocol=TCP localport=8001-8004
```

---

## Debugging

Friend should watch logs for:
- `UDP beacon: learned Node 1 at 10.12.226.231` (auto-discovery working)
- `Node 1 joined the network` (connection successful)
- `Trying Node 1 at 10.12.226.231` (manual IP mode)

Share this file with your friend!
