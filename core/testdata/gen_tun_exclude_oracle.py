import ipaddress as ip, json
def sub(bases, holes):
    out=[]
    for b in bases:
        cur=[ip.ip_network(b)]
        for h in holes:
            h=ip.ip_network(h); nxt=[]
            for c in cur:
                if c.version!=h.version or not c.overlaps(h): nxt.append(c)
                elif h.subnet_of(c) and h!=c: nxt.extend(c.address_exclude(h))
                elif c.subnet_of(h): pass
                else: nxt.append(c)
            cur=nxt
        out+=cur
    # sort per family stable by base order then address
    return [str(n) for n in sorted(out,key=lambda n:(n.version,n.network_address))]
V4=["10.0.0.0/8","100.64.0.0/10","169.254.0.0/16","172.16.0.0/12","192.168.0.0/16","224.0.0.0/4"]
V6=["fc00::/7","fe80::/10","ff00::/8"]
TUN4="172.19.0.0/30"; TUN6="fdfe:dcba:9876::/126"
END=["255.255.255.255/32","ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff/128"]
cases={
 "v4":sub(V4,[TUN4,TUN6]),
 "v46":sub(V4+V6,[TUN4,TUN6]),
 "v4_local":sub(V4,["192.168.1.0/24",TUN4,TUN6]),
 "v46_end":sub(V4+V6,[TUN4,TUN6]+END),
 "v4_hole10":sub(V4,[TUN4,TUN6,"10.1.0.0/16"]),
 "v4_hole172":sub(V4,[TUN4,TUN6,"172.19.0.4/30"]),
 "v46_fake":sub(V4+V6,[TUN4,TUN6,"fc00::/18"]),
 "v4_hole_local":sub(V4,[TUN4,TUN6,"192.168.1.0/25"]),
 "v46_hole_fd00":sub(V4+V6,[TUN4,TUN6,"fd00::/16"]),
 "v4_hole32":sub(V4,[TUN4,TUN6,"10.2.0.0/32"]),
}
for k,v in cases.items(): print(k,len(v))
json.dump(cases,open('/tmp/cases.json','w'))  # 再由使用者另存为 config_tun_exclude_oracle.json(加说明键)
