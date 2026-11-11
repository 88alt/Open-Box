#!/usr/bin/env python3
# 阶段 D 的辅助工具:拿官方 sing-box 对 testdata/config_golden.json 里的配置逐条跑 `sing-box check`。
# 用法: python3 sbcheck.py <sing-box 可执行文件> [config_golden.json] [最多条数]
#   官方 1.14.4: https://github.com/SagerNet/sing-box/releases/download/v1.14.4/sing-box-1.14.4-linux-amd64.tar.gz
#   官方 1.13.14: https://github.com/SagerNet/sing-box/releases/download/v1.13.14/sing-box-1.13.14-linux-amd64.tar.gz
# 检查前做的处理(都不是装配错误):
#   1) 剥掉官方版不认的字段:上游补丁字段 expected_status 一律剥;sing-box 1.14 才有的 tun dns_mode / dns_address /
#      include_mac_address / exclude_mac_address、规则里的 source_mac_address 只在官方版本低于 1.14 时才剥
#      (1.14 及以上官方认,原样交给 check,这些字段也就被官方校验到了);
#   2) 语料里的假 uuid / 密钥 / 证书路径换成合法值(证书用 openssl 现生成),规则集文件用占位内容现编译。
# 第二个参数是目录时,逐个读里面的 *.json(每个文件就是一份 config):用于检查自写测试向量的产出,
#   生成:OPENBOX_DUMP_DIR=<目录> go test ./internal/config/ -count=1(见 config/build_head_vectors_test.go 的 dumpConfig)。
# 只用于验证,不进发布。
import json,subprocess,os,re,collections,shutil,sys,tempfile
SB=sys.argv[1]
_v=subprocess.run([SB,'version'],capture_output=True,text=True).stdout
_m=re.search(r'version (\d+)\.(\d+)',_v)
KNOWS_114=bool(_m) and (int(_m.group(1)),int(_m.group(2)))>=(1,14)
GOLD=sys.argv[2] if len(sys.argv)>2 else os.path.join(os.path.dirname(os.path.abspath(__file__)),'..','config_golden.json')
LIMIT=int(sys.argv[3]) if len(sys.argv)>3 else 100000
work=tempfile.mkdtemp(prefix='sbcheck-')
subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',work+'/k.pem','-out',work+'/c.pem','-days','30','-subj','/CN=x'],capture_output=True)
if os.path.isdir(GOLD):
    ok=[{'want':{'config':json.load(open(os.path.join(GOLD,f)))},'error':None,'file':f} for f in sorted(os.listdir(GOLD)) if f.endswith('.json')]
else:
    d=json.load(open(GOLD))
    ok=[c for c in d['cases'] if not c['error']]
src=os.path.join(work,'ph.json'); json.dump({"version":3,"rules":[{"domain":["placeholder.invalid"]}]},open(src,'w'))
cache={}
def mk(tag,fmt):
    p=os.path.join(work,tag+('.srs' if fmt=='binary' else '.json'))
    if p in cache: return p
    if fmt=='binary':
        subprocess.run([SB,'rule-set','compile','--output',p,src],capture_output=True)
    else: shutil.copy(src,p)
    cache[p]=1; return p
errs=collections.Counter(); n=0; passed=0; examples={}; fails=[]
for i,c in enumerate(ok[:LIMIT]):
    cfg=c['want']['config']
    if not KNOWS_114:
        for k in ('dns_mode','dns_address','include_mac_address','exclude_mac_address'): cfg['inbounds'][0].pop(k,None)
    def strip(o):
        if isinstance(o,dict):
            if not KNOWS_114: o.pop('source_mac_address',None)
            for v in o.values(): strip(v)
        elif isinstance(o,list):
            for v in o: strip(v)
    strip(cfg['route']); strip(cfg['dns'])
    KEY='A'*43+'='
    UU='11111111-2222-3333-4444-555555555555'
    def fix(o):
        if isinstance(o,dict):
            o.pop('expected_status',None)
            if 'uuid' in o: o['uuid']=UU
            if 'public_key' in o: o['public_key']=('A'*43 if 'short_id' in o else KEY)
            if 'private_key' in o: o['private_key']=KEY
            if 'peer_public_key' in o: o['peer_public_key']=KEY
            if 'pre_shared_key' in o: o['pre_shared_key']=KEY
            if 'certificate_path' in o: o['certificate_path']=work+'/c.pem'
            if 'key_path' in o: o['key_path']=work+'/k.pem'
            if 'short_id' in o: o['short_id']='ab12'
            for v in o.values(): fix(v)
        elif isinstance(o,list):
            for v in o: fix(v)
    fix(cfg)
    for rs in cfg['route'].get('rule_set',[]):
        if rs['type']=='local': rs['path']=mk(re.sub(r'[^A-Za-z0-9_.-]','_',rs['tag']),rs['format'])
    cfg['experimental']['cache_file']['path']=os.path.join(work,'cache.db')
    p=os.path.join(work,'c.json'); json.dump(cfg,open(p,'w'),ensure_ascii=False)
    r=subprocess.run([SB,'check','-c',p],capture_output=True,text=True)
    n+=1
    if r.returncode==0: passed+=1
    else:
        msg=(r.stdout+r.stderr).strip().splitlines()[-1][:160]
        key=re.sub(r'\d+','N',re.sub(r'[\u4e00-\u9fff\w.-]+(?=[」\]])','X',msg))
        errs[key]+=1; examples.setdefault(key,(c.get('file',i),msg)); fails.append(msg)
print('checked',n,'passed',passed)
for k,v in errs.most_common(30): print(v,examples[k][0],examples[k][1])
# 金标准语料(config_golden.json)已知的三类「官方 1.14 比 1.13 严」的失败,见 HANDOFF 第四节 3。归并后只剩「未归类」才是新问题。
# 只对金标准语料有意义;检查自写向量产出(第二个参数是目录)时,有失败就是真问题,不归类。
if not os.path.isdir(GOLD) and fails:
    known=collections.OrderedDict([
        ('1 过滤名单未进 route.rule_set(语料输入的 allow / blocks 引用了 sets 里没有的名单)',lambda m:'rule-set not found: dns-filter-' in m),
        ('2 订阅 id 重复导致节点 DoH 服务器 tag 撞了(语料里 sub-a / b / c 重复)',lambda m:'duplicate DNS server tag: dns-sub-' in m),
        ('3 前置自定义分流 geosite 行的出口不是已知出口(DNS 侧仍写、route 侧整行跳过)',lambda m:'rule-set not found: geosite-' in m),
    ])
    left=list(fails)
    print('已知语料问题归并:')
    for name,f in known.items():
        hit=[m for m in left if f(m)]; left=[m for m in left if not f(m)]
        print(' ',len(hit),name)
    print('  未归类',len(left),'(不为 0 就是新问题,先查)')
    for m in left[:5]: print('   ',m)
