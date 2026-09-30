#!/usr/bin/env python3
"""根据随仓 Engine 契约生成 Swagger OpenAPI 文档；--check 检查是否同步。"""
import json
from pathlib import Path
import sys
root = Path(__file__).resolve().parents[1]
spec = json.loads((root / 'contracts/protocol.json').read_text())
limits = spec['limits']
def obj(properties, required=None, strict=False):
    s = {'type':'object','properties':properties}
    if required: s['required']=required
    if strict: s['additionalProperties']=False
    return s
def string(**kw): return dict(type='string', **kw)
def infer(v):
    if isinstance(v, bool): return {'type':'boolean'}
    if isinstance(v, int): return {'type':'integer'}
    if isinstance(v, str): return string()
    if isinstance(v, list): return {'type':'array','items':infer(v[0]) if v else {}}
    return obj({k:infer(x) for k,x in v.items()}, list(v))
def text_limit(key): return string(minLength=1, description=f'非空文字，UTF-8 最多 {limits[key]} 字节。')
message=obj({'role':string(enum=['system','user','assistant']),'content':text_limit('chat_message_bytes')},['role','content'],True)
format_schema=obj({'type':string(enum=['json_object','text'])},['type'],True)
requests={
 'chat':obj({'model':string(description='选择 models 列表中的模型；管理员未开启选择时使用默认模型。'),'messages':{'type':'array','minItems':1,'maxItems':limits['chat_messages'],'items':message},'stream':{'type':'boolean','enum':[False],'default':False},'max_tokens':{'type':'integer','minimum':0,'maximum':limits['chat_max_tokens'],'default':limits['chat_default_tokens'],'description':'省略或 0 使用服务端默认值。'},'temperature':{'type':'number','minimum':0,'maximum':2},'response_format':format_schema,'thinking':obj({'type':string(enum=['disabled'])},['type'],True),'enable_thinking':{'type':'boolean','enum':[False]}},['messages'],True),
 'translation':obj({'text':text_limit('translation_input_bytes'),'source_lang':string(pattern='^[A-Za-z-]{2,16}$',example='AUTO'),'target_lang':string(pattern='^[A-Za-z-]{2,16}$',example='EN')},['text','source_lang','target_lang'],True),
 'transcription':obj({'file':string(format='binary',description=f'PCM/IEEE-float RIFF/WAVE，最多 {limits["audio_file_bytes"]} 字节。'),'model':string(description='服务端覆盖此模型字段。'),'language':string(pattern='^[A-Za-z-]{2,16}$',example='zh'),'response_format':string(enum=['json'],default='json')},['file'],True)
}
summaries={'models':'查询可选聊天模型','health':'健康检查','capabilities':'查询已启用能力','cloud':'云候选','chat':'AI 联想 / 语音润色','translation':'候选翻译','transcription':'WAV 批量语音转写'}
paths={}
for key,op in spec['operations'].items():
    responses={'200':{'description':'成功','content':{'application/json':{'schema':infer(op['response']),'example':op['response']}}}}
    if op['authenticated']:
        for code,description in spec['statuses'].items():
            responses[code]={'description':description,'content':{'application/json':{'schema':{'$ref':'#/components/schemas/Error'}}}}
        for code in ['429','503']:
            responses[code]['headers']={'Retry-After':{'description':'重试等待秒数（繁忙时提供）','schema':{'type':'integer'}}}
    operation={'operationId':key,'summary':summaries[key],'tags':['系统' if key in ['health','capabilities'] else '在线输入'],'responses':responses}
    if not op['authenticated']:operation['security']=[]
    if key in requests:
        media={'schema':requests[key]}
        if 'request' in op:media['example']=op['request']
        operation['requestBody']={'required':True,'content':{op['content_type']:media}}
    if key=='cloud':
        operation['description']=op['description']+' 启用原生引擎时，无完整拼音云候选会补查覆盖整段输入的词典词条；Engine 确认拼写纠错且找到完整词条时优先采用词典结果；limit 为上限，不保证返回数量。'
        operation['parameters']=[{'name':name,'in':'query','required':name=='text','schema':schema} for name,schema in {'text':dict(text_limit('cloud_input_bytes'),example='haohaoxuexi',description='输入拼音，例如 haohaoxuexi 或 hao hao xue xi；不要输入已转换的中文。最多 256 UTF-8 字节。'),'scheme':string(enum=op['schemes'],default='pinyin'),'limit':{'type':'integer','minimum':1,'maximum':limits['cloud_candidates'],'default':5}}.items()]
    paths[op['path']]={op['method'].lower():operation}
for key,op in spec['websocket_operations'].items():
    paths[op['path']]={'get':{'operationId':key,'summary':'实时语音 WebSocket（仅文档）','tags':['实时语音'],'description':op['protocol']+'\n\nSwagger UI 不支持 WebSocket 二进制会话。使用 WSS 客户端携带设备 Bearer 令牌建立连接。'+op['close_policy']+f' 单消息最多 {limits["stream_message_bytes"]} 字节，每方向每会话最多 {limits["stream_session_bytes"]} 字节。','x-websocket':True,'responses':{'101':{'description':'WebSocket 升级成功；后续为豆包 ASR v1 二进制消息'},'400':{'description':'需要 WebSocket Upgrade'},'401':{'description':'设备令牌无效'},'503':{'description':'功能未启用或服务繁忙'}}}}
result={'openapi':'3.0.3','info':{'title':'水杉输入法后端 API','version':spec['version'],'description':'水杉输入法共通后端。点击 Authorize 填写设备令牌（不含 Bearer 前缀）。功能是否启用请查询 capabilities。JSON 请求最多 64 KiB，multipart 总体最多 16 MiB。服务不保存输入和音频。'},'servers':[{'url':'/'}],'security':[{'deviceToken':[]}],'paths':paths,'components':{'securitySchemes':{'deviceToken':{'type':'http','scheme':'bearer','description':'管理员发放的设备令牌；不是供应商密钥。'}},'schemas':{'Error':infer(spec['error_response'])}}}
# 用户体系独立于 Engine 输入协议，避免修改客户端共通契约。
user=obj({'id':string(),'display_name':string(),'created_at':string(format='date-time')})
tokens=obj({'access_token':string(),'refresh_token':string(),'token_type':string(enum=['Bearer']),'expires_in':{'type':'integer'},'user':user})
provider=string(enum=['apple','google','wechat','phone','email'])
auth_operations=[
 ('/v1/auth/providers','get','查询可用登录方式',None,obj({'providers':obj({p:{'type':'boolean'} for p in ['apple','google','wechat','phone','email']})}),False,200),
 ('/v1/auth/challenges','post','创建登录或绑定挑战',obj({'provider':provider,'target':string(description='邮箱地址或 E.164 手机号；第三方登录省略。'),'purpose':string(enum=['login','link'],default='login')},['provider'],True),obj({'challenge_id':string(),'expires_in':{'type':'integer'},'nonce':string(),'authorization_url':string()}),False,201),
 ('/v1/auth/login','post','验证凭据并登录或绑定',obj({'challenge_id':string(),'credential':string(description='六位验证码、Apple/Google ID Token 或微信授权 code。')},['challenge_id','credential'],True),tokens,False,200),
 ('/v1/auth/refresh','post','轮换用户会话令牌',obj({'refresh_token':string()},['refresh_token'],True),tokens,False,200),
 ('/v1/auth/logout','post','退出当前或全部会话',obj({'all':{'type':'boolean','default':False}},strict=True),None,True,204),
 ('/v1/users/me','get','查询当前用户和已绑定身份',None,obj({'user':user,'identities':{'type':'array','items':obj({'provider':provider,'subject':string()})}}),True,200),
 ('/v1/users/me','patch','修改当前用户昵称',obj({'display_name':string(maxLength=64)},['display_name'],True),None,True,204),
 ('/v1/users/me','delete','注销当前用户',None,None,True,204),
]
for path,method,title,body,response,protected,status in auth_operations:
    responses={str(status):{'description':'成功'}}
    if response: responses[str(status)]['content']={'application/json':{'schema':response}}
    for code in ['400','401','403','409','415','429','503']:
        responses[code]={'description':'请求无效、凭据失效、需要重新登录、身份冲突、格式错误、限流或功能不可用。','content':{'application/json':{'schema':{'$ref':'#/components/schemas/Error'}}}}
    op={'summary':title,'tags':['用户体系'],'security':[{'userSession':[]}] if protected else [],'responses':responses,'description':'JSON 请求最多 16 KiB。绑定身份需要在挑战创建和验证时携带同一用户的会话令牌；绑定和注销要求最近 10 分钟内登录。设备令牌不能用于用户管理。'}
    if body: op['requestBody']={'required':True,'content':{'application/json':{'schema':body}}}
    paths.setdefault(path,{})[method]=op
result['security']=[{'deviceToken':[]},{'userSession':[]}]
result['components']['securitySchemes']['userSession']={'type':'http','scheme':'bearer','description':'登录返回的 access_token，不是 refresh_token 或供应商密钥。'}
result['info']['description']+=' 用户接口详见用户体系标签；登录成功后也可使用用户 access_token 调用在线输入接口。'
# 跨端用户数据不属于 Engine 在线输入契约。
preference_fields=json.loads((root/'internal/account/preferences_fields.json').read_text())
settings=obj(preference_fields,strict=True)
preferences=obj({'revision':{'type':'integer','format':'int64','minimum':0},'settings':settings},['revision','settings'],True)
clipboard_item=obj({'id':string(),'text':string(description='最多 4000 个 UTF-16 单元，不允许空白或 NUL。'),'updated_at':string(format='date-time')})
shared_operations=[
 ('/v1/users/me/preferences','get','读取跨端偏好',None,preferences,200,'新用户返回 revision=0、空 settings。仅保存白名单字段，不保存凭据或本机路径。'),
 ('/v1/users/me/preferences','put','替换跨端偏好',preferences,preferences,200,'请求最多 1 MiB；revision 必须匹配当前版本，否则返回 409。成功后版本加一；未提交的字段被移除。'),
 ('/v1/users/me/preferences/schema','get','查询可同步偏好字段',None,obj({'fields':obj({},strict=False),'maximum_bytes':{'type':'integer'},'update_mode':string(enum=['replace']),'revision_required':{'type':'boolean'}}),200,'返回允许同步的字段及类型；不包含本机配置值。'),
 ('/v1/users/me/clipboard','get','查询和搜索云端剪贴板',None,obj({'enabled':{'type':'boolean'},'items':{'type':'array','maxItems':50,'items':clipboard_item}}),200,'按最近添加顺序返回最多 50 条。q 为大小写不敏感的原文子串，最多 1024 UTF-8 字节。'),
 ('/v1/users/me/clipboard','post','添加云端剪贴板条目',obj({'text':clipboard_item['properties']['text']},['text'],True),clipboard_item,200,'必须显式开启同步，否则返回 403。请求最多 32 KiB，文本最多 4000 个 UTF-16 单元。重复文本保留 ID 并移动到最前；超过 50 条移除最旧条目。'),
 ('/v1/users/me/clipboard','delete','清空云端剪贴板',None,None,204,'只清空当前用户云端记录，不操作客户端系统剪贴板。'),
 ('/v1/users/me/clipboard/{id}','delete','删除云端剪贴板条目',None,None,204,'条目不存在或属于其他用户均返回 404。'),
 ('/v1/users/me/clipboard/settings','put','开启或关闭云端剪贴板',obj({'enabled':{'type':'boolean'}},['enabled'],True),obj({'enabled':{'type':'boolean'}}),200,'默认关闭，必须由用户显式开启；关闭会删除该用户全部云端剪贴板记录。请求最多 16 KiB。'),
]
for path,method,title,body,response,status,description in shared_operations:
    responses={str(status):{'description':'成功'}}
    if response: responses[str(status)]['content']={'application/json':{'schema':response}}
    for code,reason in [('400','参数无效'),('401','需要有效用户会话'),('403','未开启剪贴板同步'),('404','条目不存在'),('409','偏好版本冲突'),('415','需要 application/json'),('503','用户数据服务不可用')]:
        responses[code]={'description':reason,'content':{'application/json':{'schema':{'$ref':'#/components/schemas/Error'}}}}
    op={'summary':title,'tags':['用户同步数据'],'description':description+' 设备令牌不能访问用户同步数据；注销账号会级联删除这些数据。','security':[{'userSession':[]}],'responses':responses}
    if body: op['requestBody']={'required':True,'content':{'application/json':{'schema':body}}}
    if '{id}' in path: op['parameters']=[{'name':'id','in':'path','required':True,'schema':string()}]
    if method=='get' and path.endswith('/clipboard'): op['parameters']=[{'name':'q','in':'query','schema':string()}]
    paths.setdefault(path,{})[method]=op
result['info']['description']=result['info']['description'].replace('服务不保存输入和音频。','在线输入接口不保存输入和音频；用户同步接口按用户操作保存偏好及显式上传的数据。')
# 无状态 Engine 查询；算法及发布词库由原生公共库提供。
candidate=obj({'code':string(),'canonical_pinyin':string(),'word':string(),'weight':{'type':'integer','format':'int64'},'fixed_position':{'type':'integer'}})
candidate_response=obj({'candidates':{'type':'array','items':candidate},'raw_segmentation':string(),'normalized_segmentation':string()})
paths['/v1/input/capabilities']={'get':{'summary':'查询公共引擎配置能力','tags':['公共输入引擎'],'description':'返回 Engine 和词库是否配置，以及支持的输入方案、双拼方案和候选上限。','responses':{'200':{'description':'成功'},'401':{'description':'缺少有效令牌'}}}}
input_titles={'romaji':'日语罗马字与假名转换','japanese':'日语罗马字候选查询','convert':'简体转繁体（OpenCC s2t）','annotate':'纯汉字词组注音','unicode':'Unicode 码点候选','datetime':'日期时间候选','english':'英文前缀补全','gloss':'中英双向释义','emoji':'Emoji 拼音查询','kaomoji':'颜文字拼音查询','jianpin':'简拼候选','candidates':'本地词库候选','segmentation':'输入方案切分','quick':'快捷短语候选','helpcode':'汉字辅助码'}
for operation,title in input_titles.items():
    fields={'text':string(minLength=1,description='查询文字；输入码最多 256 ASCII 字符，其他文字最多 8192 UTF-8 字节。'),'limit':{'type':'integer','minimum':1,'maximum':200,'default':20}}
    if operation in ['emoji','kaomoji','jianpin','candidates','segmentation']:
        fields.update({'scheme':string(enum=['pinyin','shuangpin','wubi'],default='pinyin'),'profile':string(enum=['xiaohe','ziranma','shoudao','microsoft'],default='xiaohe')})
    if operation=='datetime': fields.update({'time':string(format='date-time',description='RFC 3339 参考时刻，省略使用当前时间。'),'timezone':string(default='UTC',example='Asia/Shanghai',description='IANA 时区。')})
    if operation=='gloss': fields['direction']=string(enum=['en-zh','zh-en'],default='en-zh')
    if operation=='romaji': fields['direction']=string(enum=['romaji-hiragana','hiragana-katakana','kana-romaji'],default='romaji-hiragana')
    if operation=='helpcode': fields['schema']=string(enum=['lantian','ziranma','shouyou2_0','shouyouplus','xiaohe'],default='lantian')
    response=candidate_response
    if operation=='romaji': response=obj({'text':string(),'pending':string(),'complete':{'type':'boolean'}})
    if operation=='japanese': response=obj(dict(candidate_response['properties'],hiragana=string(),pending=string(),complete={'type':'boolean'}))
    if operation in ['gloss','helpcode','convert']: response=obj({'text':string(),'schema':string(),'conversion':string()})
    if operation=='annotate': response=obj({'code':string(),'word':string()})
    if operation=='segmentation': response=obj({'raw':string(),'normalized':string()})
    responses={'200':{'description':'成功','content':{'application/json':{'schema':response}}}}
    for code,description in [('400','参数无效'),('401','缺少有效设备或用户令牌'),('429','限流'),('502','原生查询失败'),('503','原生 Engine 或数据未配置，或服务繁忙'),('504','查询超时')]: responses[code]={'description':description}
    paths['/v1/input/'+operation]={'post':{'summary':title,'tags':['公共输入引擎'],'description':'复用固定版本 Engine，无状态查询，不写入用户学习记录。请求最多 64 KiB；不接受资源路径、运行命令或上游地址。','requestBody':{'required':True,'content':{'application/json':{'schema':obj(fields,['text'],True)}}},'responses':responses}}
for kind,title in [('emoji','Emoji'),('kaomoji','颜文字'),('symbols','符号')]:
    paths['/v1/catalog/'+kind]={'get':{'summary':title+'目录与分类','tags':['公共输入引擎'],'description':'返回按发布词库顺序排列的条目、全部分类及数量；q 搜索文字或关键词，category 精确匹配分类。','parameters':[{'name':k,'in':'query','schema':v} for k,v in {'q':string(),'category':string(),'offset':{'type':'integer','minimum':0,'maximum':1000000,'default':0},'limit':{'type':'integer','minimum':1,'maximum':200,'default':50}}.items()],'responses':{'200':{'description':'成功','content':{'application/json':{'schema':obj({'items':{'type':'array','items':obj({'text':string(),'category':string(),'parent_category':string(),'keywords':string()})},'categories':{'type':'array','items':obj({'name':string(),'parent':string(),'count':{'type':'integer'}})},'offset':{'type':'integer'},'has_more':{'type':'boolean'}})}}},'400':{'description':'参数无效'},'401':{'description':'缺少有效令牌'},'503':{'description':'词库不可用'}}}}
entry=obj({'id':string(),'kind':string(enum=['pinyin','wubi','english','quick']),'code':string(),'word':string(),'weight':{'type':'integer','format':'int64'},'revision':{'type':'integer','format':'int64'},'updated_at':string(format='date-time')})
position_fields={'context':string(description='Engine 返回的固定位置上下文，最多 512 UTF-8 字节。'),'code':string(description='候选规范编码，最多 512 UTF-8 字节。'),'word':string(description='候选文字，最多 2048 UTF-8 字节。')}
position=obj(dict(position_fields,position={'type':'integer','minimum':0,'maximum':5}),['context','code','word','position'])
selection=obj({'context':string(),'code':string(),'word':string(),'count':{'type':'integer','minimum':0,'maximum':10}})
entry['properties']['user_inserted']={'type':'boolean','description':'省略表示用户新增；false 表示基础候选的调频覆盖。'}
change=obj({'reset':{'type':'boolean','description':'true 表示完整状态已被替换；客户端应丢弃词库缓存并重新读取完整快照。'},'ranking':{'type':'array','items':entry},'selection':selection,'position':position,'revision':{'type':'integer','format':'int64'},'previous':dict(entry,nullable=True),'replacement':dict(entry,nullable=True)})
entry_body=obj({'code':string(),'word':string(),'weight':{'type':'integer','format':'int64','minimum':0,'default':10}},['code','word'],True)
update_body=obj(dict(entry_body['properties'],revision={'type':'integer','format':'int64','minimum':1}),['code','word','revision'],True)
page_params=[{'name':'offset','in':'query','schema':{'type':'integer','minimum':0,'maximum':1000000,'default':0}},{'name':'limit','in':'query','schema':{'type':'integer','minimum':1,'maximum':200,'default':200}}]
dictionary_ops=[
 ('/v1/users/me/dictionary/positions','get','查询用户固定候选位置',None,obj({'positions':{'type':'array','items':position},'has_more':{'type':'boolean'},'offset':{'type':'integer'}}),200,'按上下文、位置排序；context 可选精确筛选。'),
 ('/v1/users/me/dictionary/positions','put','设置固定候选位置',obj(dict(position_fields,revision={'type':'integer','format':'int64','minimum':0},position={'type':'integer','minimum':1,'maximum':5}),['revision','context','code','word','position'],True),obj({'revision':{'type':'integer','format':'int64'}}),200,'revision 必须匹配用户词库总版本。每个上下文五个位置；占用同一位置会替换旧设置。同一候选移动时释放旧位置。context、code、word 总计最多 2048 UTF-8 字节。'),
 ('/v1/users/me/dictionary/positions','delete','清除固定候选位置',obj(dict(position_fields,revision={'type':'integer','format':'int64','minimum':0}),['revision','context','code','word'],True),obj({'revision':{'type':'integer','format':'int64'}}),200,'revision 必须匹配用户词库总版本；清除记录以 position=0 写入变更日志。context、code、word 总计最多 2048 UTF-8 字节。'),
 ('/v1/users/me/dictionary/candidates','post','查询个人词库与基础词库合并候选',obj({'text':string(),'kind':string(enum=['pinyin','wubi','english','quick','jianpin'],default='pinyin'),'scheme':string(enum=['pinyin','shuangpin','wubi'],default='pinyin'),'profile':string(enum=['xiaohe','ziranma','shoudao','microsoft'],default='xiaohe'),'limit':{'type':'integer','minimum':1,'maximum':200,'default':20}},['text'],True),obj(dict(candidate_response['properties'],context=string(description='固定位置操作使用的上下文键。'),revision={'type':'integer','format':'int64'})),200,'同一数据库快照读取当前用户覆盖及 revision，再由 Engine 将覆盖和删除记录回放到临时词库副本。只读公共基础词库；副本在查询完成或失败后清理；返回完整词库版本以供客户端判断缓存。'),
 ('/v1/users/me/dictionaries/{kind}/import-hans','post','纯汉字词组注音导入',obj({'text':string(description='每行一个纯汉字词组，最多 128 个汉字。'),'weight':{'type':'integer','minimum':0,'default':10}},['text'],True),obj({'imported':{'type':'integer'},'revision':{'type':'integer','format':'int64'}}),200,'仅支持 pinyin 类别；1–500 个词组、JSON 最多 64 KiB；沿用 cpp-pinyin 词组注音并经 Engine 校验，任何失败均不写入。'),
 ('/v1/users/me/dictionaries/{kind}','get','查询个人词条',None,obj({'entries':{'type':'array','items':entry},'has_more':{'type':'boolean'},'offset':{'type':'integer'}}),200,'支持 q 原文子串搜索，最多 1024 UTF-8 字节；按编码和文字稳定排序。只查询当前用户创建的词条。'),
 ('/v1/users/me/dictionaries/{kind}','post','新增个人词条',entry_body,change,201,'复用 Engine 规范化与校验；同类编码和文字重复返回 409；每个用户最多 100000 个词条。'),
 ('/v1/users/me/dictionaries/{kind}/{id}','put','修改个人词条',update_body,change,200,'revision 必须匹配该词条版本，否则返回 409；不存在或属于其他用户均返回 404。'),
 ('/v1/users/me/dictionaries/{kind}/{id}','delete','删除个人词条',obj({'revision':{'type':'integer','format':'int64','minimum':1}},['revision'],True),change,200,'要求该词条当前 revision；删除保留变更记录以供其他设备同步。'),
 ('/v1/users/me/dictionaries/{kind}/import','post','批量导入个人词条',obj({'text':string(description='三列 TSV，权重沿用 Engine 的 1–100000000 范围。'),'format':string(enum=['standard','windows'],default='standard')},['text'],True),obj({'imported':{'type':'integer'},'revision':{'type':'integer','format':'int64'}}),200,'JSON 最多 64 KiB，一次 1–500 条。standard 三列为文字、编码、权重；windows 的英文和快捷短语三列为编码、文字、权重，拼音和五笔为文字、编码、权重。权重必须为 Engine 支持的 1–100000000，不接受旧文件中的零权重；任何无效、重复或超配额均全部回滚。'),
 ('/v1/users/me/dictionaries/{kind}/export','get','导出个人词条',None,None,200,'standard 导出当前用户新增词条，列为文字、编码、权重。windows 匹配 Windows 导出列顺序：英文和快捷短语为编码、文字、权重；拼音和五笔为文字、编码、权重。windows 拼音导出所有多字 upsert 覆盖（包含调频），其余类别仅导出用户新增词条；均排除删除记录。一条 SELECT 取得一致快照。'),
 ('/v1/users/me/dictionary/changes','get','读取个人词库增量变更',None,obj({'changes':{'type':'array','items':change},'next':{'type':'integer','format':'int64'},'has_more':{'type':'boolean'}}),200,'after 为已消费的用户词库版本，默认 0；返回更大版本的有序变更，next 可用于继续读取。删除记录 replacement 为 null。')
]
ranking_query=next(body for path,method,title,body,response,status,description in dictionary_ops if path.endswith('/dictionary/candidates'))
ranking_action=obj({'code':string(),'word':string(),'mode':string(enum=['disabled','pin','halve','linear','promote'],default='pin'),'linear_step':{'type':'integer','minimum':1,'maximum':100,'default':1},'trigger_count':{'type':'integer','minimum':1,'maximum':10,'default':1},'force_top':{'type':'boolean','default':False}},['code','word'],True)
dictionary_ops.append(('/v1/users/me/dictionary/ranking','post','调整用户候选排序',obj({'revision':{'type':'integer','format':'int64','minimum':0},'query':ranking_query,'action':ranking_action},['revision','query','action'],True),obj({'updates':{'type':'array','items':entry},'selection':selection,'changed':{'type':'boolean'},'revision':{'type':'integer','format':'int64'}}),200,'沿用 Engine 调频算法；仅支持拼音、双拼、五笔、简拼与英文。code、word 必须匹配当前候选，合计最多 1536 UTF-8 字节。revision 为用户词库总版本；每个成功操作递增版本，包括未达到触发次数的选择。计数和权重在同一用户事务保存，设备令牌不能调用；基础候选的权重覆盖不成为个人新增词条。'))
dictionary_ops.append(('/v1/users/me/dictionary/candidates','delete','删除当前用户的候选',obj({'revision':{'type':'integer','format':'int64','minimum':0},'query':ranking_query,'code':string(),'word':string()},['revision','query','code','word'],True),change,200,'精确匹配当前合并候选的编码和文字，调用 Engine 删除事务并保存当前用户删除记录；不修改公共词库。支持拼音、双拼、五笔、简拼和英文；非英文单字沿用 Windows 保护规则，不能删除。code 与 word 合计最多 1536 UTF-8 字节；revision 必须匹配用户词库总版本。删除用户新增候选时一并移除个人词条。'))
dictionary_ops.append(('/v1/users/me/dictionary/snapshot','get','导出完整用户词库状态',None,None,200,'从单条数据库查询的一致快照流式导出 NDJSON。header 包含 format=msime-dictionary-snapshot、version=1 和用户词库总 revision；后续 entry、overlay（含 deleted）、position、selection 记录保存个人词条、权重覆盖与删除、固定位置、触发计数。最后 footer 的 records 是此前记录数，sha256 是此前所有行（包含每行末尾 LF）的 SHA-256；没有有效 footer 的下载不完整。文件不含用户账号标识、会话或供应商凭据。'))
dictionary_ops.append(('/v1/users/me/dictionary/snapshot','put','原子恢复完整用户词库状态',string(format='binary'),obj({'revision':{'type':'integer','format':'int64'},'reset':{'type':'boolean','enum':[True]}}),200,'上传完整导出 NDJSON 文件，服务端检查记录格式、完整性和 Engine 词条规则后原子替换当前用户词库。revision 查询参数必须匹配目标用户当前总版本；源文件版本不能代替此参数。成功后生成新词条 ID，并写入 reset 变更，客户端需重新同步。单次最多 512 MiB、最多 100000 个人词条，处理期限 120 秒；每用户每分钟最多 5 次尝试，每服务进程同时处理一次恢复。任何失败都不改变目标用户状态。'))
dictionary_ops.append(('/v1/users/me/dictionaries/{kind}/catalog','get','分页查询基础词库与个人覆盖',None,obj({'entries':{'type':'array','items':obj({'kind':string(),'code':string(),'word':string(),'weight':{'type':'integer','format':'int64'}})},'offset':{'type':'integer'},'has_more':{'type':'boolean'},'revision':{'type':'integer','format':'int64'},'normalized':string()}),200,'查询当前用户与基础词库合并后的管理条目，包含调频覆盖并排除删除记录。拼音按 Engine 全拼/双拼规范编码精确查询；英文、五笔和快捷短语按前缀查询。仅快捷短语允许空 q 查询全部；按 Windows 管理器的权重及编码顺序分页，排序不应用候选固定位置。'))
dictionary_ops.append(('/v1/users/me/dictionaries/{kind}/edit','post','显式编辑合并词库中的词条',obj({'revision':{'type':'integer','format':'int64','minimum':0},'previous':obj({'code':string(),'word':string()},['code','word'],True),'replacement':dict(obj({'code':string(),'word':string(),'weight':{'type':'integer','format':'int64','minimum':1,'maximum':100000000}},['code','word','weight'],True),nullable=True)},['revision','previous','replacement'],True),change,200,'使用管理目录返回的精确 code 和 word 定位条目；revision 为目标用户当前词库总版本。replacement 为新编码、文字和权重；显式 null 表示删除，包括管理器中的单字条目。更改为已存在的编码和文字组合返回 409。个人新增词条保留 ID；基础词条只形成当前用户覆盖，不写公共词库，也不变成个人新增词条。所有变更写入增量记录和完整快照。'))
for path,method,title,body,response,status,description in dictionary_ops:
    parameters=[]
    if '{kind}' in path: parameters.append({'name':'kind','in':'path','required':True,'schema':string(enum=['pinyin','wubi','english','quick'])})
    if '{id}' in path: parameters.append({'name':'id','in':'path','required':True,'schema':string()})
    if method=='get' and path.endswith('{kind}'): parameters+=page_params+[{'name':'q','in':'query','schema':string()}]
    if path.endswith('/positions') and method=='get': parameters+=page_params+[{'name':'context','in':'query','schema':string()}]
    if path.endswith('/catalog'):
        parameters+=page_params+[{'name':'q','in':'query','schema':string()},{'name':'scheme','in':'query','schema':string(enum=['pinyin','shuangpin'],default='pinyin')},{'name':'profile','in':'query','schema':string(enum=['xiaohe','ziranma','shoudao','microsoft'],default='xiaohe')}]
    if path.endswith('/changes'): parameters+=[page_params[1],{'name':'after','in':'query','schema':{'type':'integer','format':'int64','minimum':0,'default':0}}]
    responses={str(status):{'description':'成功'}}
    if response: responses[str(status)]['content']={'application/json':{'schema':response}}
    if path.endswith('/snapshot') and method=='put':
        parameters.append({'name':'revision','in':'query','required':True,'schema':{'type':'integer','format':'int64','minimum':0}})
        responses['413']={'description':'快照超过 512 MiB'}
    if path.endswith('/snapshot') and method=='get': responses['200']['content']={'application/x-ndjson':{'schema':string()}}
    if path.endswith('/export'): parameters.append({'name':'format','in':'query','schema':string(enum=['standard','windows'],default='standard')})
    if path.endswith('/export'): responses['200']['content']={'text/plain':{'schema':string()}}
    for code,reason in [('400','参数或词条无效'),('401','需要有效用户会话'),('404','类别或词条不存在'),('409','版本冲突、重复词条或用户配额已满'),('415','需要 application/json'),('429','限流'),('502','Engine 查询失败'),('503','Engine 或用户数据服务不可用'),('504','操作超时')]: responses[code]={'description':reason}
    operation={'summary':title,'tags':['用户词库'],'security':[{'userSession':[]}],'description':description+' 原生校验沿用公共 Engine 规则；快捷短语最多 199 个 UTF-16 单元。设备令牌不能访问用户词库。','responses':responses,'parameters':parameters}
    if body: operation['requestBody']={'required':True,'content':{'application/json':{'schema':body}}}
    if path.endswith('/snapshot') and method=='put':
        operation['requestBody']['content']={'application/x-ndjson':{'schema':body}}
        operation['responses']['415']={'description':'需要 application/x-ndjson'}
        operation['responses']['503']={'description':'恢复忙碌或用户数据服务不可用；忙碌时返回 Retry-After: 5'}
    paths.setdefault(path,{})[method]=operation
skin_resource=obj({'path':string(),'size':{'type':'integer'},'sha256':string(),'media_type':string(),'url':string()})
skin_colors=obj({k:string() for k in ['accent','selected','hover','surface','border','text','number','translation']}|{'show_selected_bar':{'type':'boolean'}})
toolbar_colors=obj({k:string() for k in ['background','border','handle','divider','icon','hover']})
client_only='仅数据库皮肤包（客户端方言）提供；'
skin=obj({'schema_version':{'type':'integer','enum':[1]},'id':string(),'name':string(),'version':string(),'author':string(),'description':string(),'base':string(enum=['fluent','wechat','graphite','willow_green','system','shuishan','light','paper','night','ink'],description='内置与 skins_root 皮肤包沿用 Windows 方言，base 为四个内置 ID 之一；数据库皮肤包沿用客户端方言，base 为 system 或内置全局主题；清单中的 fluent 是 system 的别名，返回 system。'),'builtin':{'type':'boolean'},'toolbar_stylesheet':string(),'preview':string(),'supports':obj({'layouts':{'type':'array','items':string(enum=['horizontal','vertical'])},'themes':{'type':'array','items':string(enum=['dark','light'])}}),'candidate_window':obj({'min_width_dip':{'type':'number'},'corner_radius_dip':{'type':'number','minimum':0,'maximum':32,'description':client_only+'省略时沿用宿主圆角。'},'decoration':obj({'top_inset_dip':{'type':'number'},'width_dip':{'type':'number'},'image':string(description=client_only+'装饰图的包内路径；省略时客户端在有装饰带时改用 preview 图片。'),'align':string(enum=['left','center','right'],description=client_only+'默认 right。')}),'background':obj({'image':string(),'fit':string(enum=['cover','contain','stretch']),'opacity':{'type':'number','minimum':0,'maximum':1}},['image','fit','opacity'])}),'candidate':obj({'dark':skin_colors,'light':skin_colors}),'toolbar':obj({'corner_radius_dip':{'type':'number','minimum':0,'maximum':32},'dark':toolbar_colors,'light':toolbar_colors}),'license':obj({'code':string(),'assets':string(),'source':string()}),'resources':{'type':'array','items':skin_resource}})
skin_paths=[
 ('/v1/skins','内置和自定义皮肤目录',obj({'skins':{'type':'array','items':skin},'invalid_packages':{'type':'integer'},'license_url':string(),'source_url':string()})),
 ('/v1/skins/{id}','皮肤元数据和资源清单',skin),
 ('/v1/skins/source','内置皮肤固定来源与文件摘要',{'type':'object'}),
 ('/v1/skins/license','内置皮肤许可证',None),
 ('/v1/skins/{id}/resources/{resource}','下载皮肤资源',None)
]
for path,title,response in skin_paths:
    parameters=[]
    if '{id}' in path: parameters.append({'name':'id','in':'path','required':True,'schema':string(pattern='^[a-z0-9][a-z0-9._-]{0,63}$')})
    if '{resource}' in path: parameters.append({'name':'resource','in':'path','required':True,'schema':string(description='皮肤内相对路径，可包含子目录；仅返回资源清单中的 CSS、图片、字体及 skin.toml。')})
    if path=='/v1/skins': parameters=[{'name':'layout','in':'query','schema':string(enum=['horizontal','vertical'])},{'name':'theme','in':'query','schema':string(enum=['dark','light'])}]
    success={'description':'成功'}
    if response: success['content']={'application/json':{'schema':response}}
    else: success['content']={'text/plain' if path.endswith('/license') else 'application/octet-stream':{'schema':string()}}
    paths[path]={'get':{'summary':title,'tags':['皮肤'],'parameters':parameters,'description':'需要设备或用户令牌；内置皮肤随服务提供，自定义目录由管理员 skins_root 配置，启用用户体系时再并入数据库中已发布的候选框皮肤包（按客户端 msime-skins 规则逐次校验）。同一 ID 同时出现在 skins_root 和数据库时两边都不提供，计 1 个 invalid_packages；数据库不可用时返回 503。无效皮肤不进入列表，计入 invalid_packages；不暴露服务器路径或解析错误详情。单资源最多 4 MiB，单包最多 16 MiB、512 个目录条目。下载保留原始文件字节与摘要，客户端仍使用其皮肤 CSS 隔离规则。','responses':{'200':success,'400':{'description':'筛选参数无效'},'401':{'description':'缺少有效令牌'},'404':{'description':'皮肤、资源不存在或不安全'},'503':{'description':'皮肤目录不可用'}}}}
# User-created Apple keyboard designs are separate from the desktop CSS catalog.
community_design = obj({
    **{key: {'type':'integer','minimum':0,'maximum':16777215} for key in ['background','keyBackground','keyForeground','accent','actionBackground','gradientEnd','customBorderColor']},
    **{key: {'type':'number','minimum':lo,'maximum':hi} for key,lo,hi in [('cornerRadius',0,20),('borderWidth',0,2),('shadow',0,.4),('keyOpacity',.25,1),('patternOpacity',0,.5),('photoShade',0,.8),('photoPosition',0,1)]},
    'keyShape': {'type':'string','enum':['rounded','capsule','ticket','pebble']},
    'keyMaterial': {'type':'string','enum':['flat','raised','glass','paper']},
    'pattern': {'type':'integer','minimum':0,'maximum':3}, 'monospaced':{'type':'boolean'}, 'gradientHorizontal':{'type':'boolean'},
    'photo':{'type':'string','format':'byte','description':'JPEG, at most 512000 decoded bytes, at most 1024 pixels per axis'}
}, ['background','keyBackground','keyForeground','accent','actionBackground','cornerRadius','borderWidth','shadow','pattern','monospaced'], True)
community_skin = obj({'id':string(),'name':string(),'description':string(),'author':string(),'design':community_design,'downloads':{'type':'integer'},'rating_count':{'type':'integer'},'rating_average':{'type':'number'},'owned':{'type':'boolean'},'my_rating':{'type':'integer'}})
for path,method,title,body,response,status in [
 ('/v1/community/skins','get','浏览用户皮肤',None,obj({'skins':{'type':'array','items':community_skin},'has_more':{'type':'boolean'}}),'200'),
 ('/v1/community/skins','post','发布用户皮肤',obj({'id':string(format='uuid'),'name':string(maxLength=32),'description':string(maxLength=280),'design':community_design},['id','name','description','design'],True),obj({'id':string()}),'201'),
 ('/v1/community/skins/{id}','get','用户皮肤详情',None,community_skin,'200'),
 ('/v1/community/skins/{id}','delete','作者下架皮肤',None,obj({'deleted':{'type':'boolean'}}),'200'),
 ('/v1/community/skins/{id}/download','post','下载皮肤并去重计数',None,obj({'design':community_design}),'200'),
 ('/v1/community/skins/{id}/rating','put','提交或修改评分',obj({'stars':{'type':'integer','minimum':1,'maximum':5}},['stars'],True),obj({'stars':{'type':'integer'}}),'200')
]:
    parameters=[]
    if '{id}' in path: parameters.append({'name':'id','in':'path','required':True,'schema':string(format='uuid')})
    elif method=='get': parameters=[{'name':'q','in':'query','schema':string(maxLength=128)},{'name':'offset','in':'query','schema':{'type':'integer','minimum':0,'maximum':100000,'default':0}}]
    operation={'summary':title,'tags':['皮肤社区'],'security':[] if method=='get' else [{'userSession':[]}], 'parameters':parameters,
      'description':'仅支持数据型 Apple 键盘 v1。发布最多 50 款，重试使用相同 UUID；下载人数按账号去重，评分需先下载且不能自评。列表每页 20 条，不包含照片字节。详见 docs/skin-community.md。',
      'responses':{status:{'description':'成功','content':{'application/json':{'schema':response}}},**{c:{'description':m} for c,m in [('400','参数无效'),('401','需要用户登录'),('403','尚未下载或正在评价自己的作品'),('404','皮肤不存在或非作者'),('409','发布配额已满或 UUID 冲突'),('429','请求过多'),('503','服务不可用')]}}}
    if body: operation['requestBody']={'required':True,'content':{'application/json':{'schema':body}}}
    if path=='/v1/community/skins' and method=='post': operation['responses']['200']={'description':'同一发布请求的安全重试','content':{'application/json':{'schema':response}}}
    paths.setdefault(path,{})[method]=operation

shared_word = obj({'kind':string(enum=['pinyin','wubi','english','quick']),'code':string(maxLength=512),'word':string(maxLength=2048),'weight':{'type':'integer','minimum':0}},['kind','code','word','weight'],True)
resource_content = obj({'entries':{'type':'array','minItems':1,'maxItems':128,'items':shared_word},'prompt':string(minLength=1,maxLength=2000)},[],True)
resource = obj({'id':string(format='uuid'),'kind':string(enum=['dictionary','reply']),'name':string(),'description':string(),'author':string(),'content':resource_content,'revision':{'type':'integer'},'saves':{'type':'integer'},'saved':{'type':'boolean'},'owned':{'type':'boolean'},'rating_count':{'type':'integer'},'rating_average':{'type':'number'},'my_rating':{'type':'integer'}})
for path,method,title,body,response in [
 ('/v1/community/resources','get','浏览词库和回复模板',None,obj({'items':{'type':'array','items':resource},'has_more':{'type':'boolean'}})),
 ('/v1/community/resources','post','发布或更新词库与回复模板',obj({'id':string(format='uuid'),'kind':string(enum=['dictionary','reply']),'name':string(minLength=1,maxLength=32),'description':string(maxLength=280),'content':resource_content,'revision':{'type':'integer','minimum':0}},['id','kind','name','description','content','revision'],True),obj({'id':string(),'revision':{'type':'integer'}})),
 ('/v1/community/resources/{id}','get','作品内容与版本',None,resource),
 ('/v1/community/resources/{id}','delete','作者下架作品',None,obj({'deleted':{'type':'boolean'}})),
 ('/v1/community/resources/{id}/apply','post','原子导入词包到个人词库（同词条更新权重，保留其他词条）',obj({'resource_revision':{'type':'integer','minimum':1},'dictionary_revision':{'type':'integer','minimum':0}},['resource_revision','dictionary_revision'],True),obj({'revision':{'type':'integer'},'imported':{'type':'integer'},'resource_revision':{'type':'integer'}})),
 ('/v1/community/resources/{id}/save','put','收藏或取消收藏',obj({'saved':{'type':'boolean'}},['saved'],True),obj({'saved':{'type':'boolean'}})),
 ('/v1/community/resources/{id}/rating','put','评分',obj({'stars':{'type':'integer','minimum':1,'maximum':5}},['stars'],True),obj({'stars':{'type':'integer'}}))
]:
    parameters=[]
    if '{id}' in path: parameters.append({'name':'id','in':'path','required':True,'schema':string(format='uuid')})
    elif method=='get': parameters=[{'name':'kind','in':'query','required':True,'schema':string(enum=['dictionary','reply'])},{'name':'scope','in':'query','schema':string(enum=['','saved','mine'])},{'name':'q','in':'query','schema':string(maxLength=128)},{'name':'offset','in':'query','schema':{'type':'integer','minimum':0,'maximum':1000000}}]
    operation={'summary':title,'tags':['创作社区'],'security':[] if method=='get' else [{'userSession':[]}],'parameters':parameters,
      'description':'发现和详情公开；saved/mine 范围需要用户会话。词库仅携带显式选定的 1–128 条记录，由 Engine 校验；回复仅携带提示词（不含密钥）。每账号最多 50 份。新建 revision=0，更新携带当前 revision，冲突返回 409；相同内容重试不增加版本。收藏按账号去重；收藏后可评分，不允许自评。查看版本不会自动覆盖个人词库。',
      'responses':{'200':{'description':'成功','content':{'application/json':{'schema':response}}},**{code:{'description':message} for code,message in [('400','内容无效'),('401','需要登录'),('403','需先收藏且不能自评'),('404','作品不存在或非作者'),('409','版本冲突或达到上限'),('429','请求过多'),('503','服务不可用')]}}}
    if body: operation['requestBody']={'required':True,'content':{'application/json':{'schema':body}}}
    if method=='post': operation['responses']['201']=operation['responses']['200']
    paths.setdefault(path,{})[method]=operation

# Anonymous website word form (msime-web#213). Errors here use a plain-string error plus code, which the website form reads.
word_error=obj({'error':string(description='可直接展示给用户的中文说明。'),'code':string()},['error','code'])
word_rejected=obj({'error':string(),'code':string(enum=['invalid_entries']),'rejected':{'type':'array','items':obj({'index':{'type':'integer','minimum':0,'description':'entries 中的下标。'},'code':string(enum=['word_required','invalid_word','word_too_long','pinyin_required','invalid_pinyin','invalid_syllable','syllable_count_mismatch','duplicate_entry','already_listed']),'reason':string(description='可直接展示在该行旁的中文说明。')},['index','code','reason'])}},['error','code'])
def word_response(description, schema=word_error, retry=False):
    r={'description':description,'content':{'application/json':{'schema':schema}}}
    if retry: r['headers']={'Retry-After':{'description':'重试等待秒数','schema':{'type':'integer'}}}
    return r
paths['/v1/community/word-submissions']={
 'get':{'summary':'查询官网词条提交是否开放','tags':['词条提交'],'security':[],
  'description':'匿名接口，无需令牌。未配置时 enabled=false、site_key 为空字符串。site_key 是 Cloudflare Turnstile 站点密钥（公开值）。',
  'responses':{'200':{'description':'成功','content':{'application/json':{'schema':obj({'enabled':{'type':'boolean'},'site_key':string()},['enabled','site_key'])}}}}},
 'post':{'summary':'匿名提交词条到 msime-customdict 滚动 Pull Request','tags':['词条提交'],'security':[],
  'description':'仅接受 allowed_origins 中网站发出的浏览器请求（必须携带 Origin）。请求体最多 16 KiB。服务端依次校验词条、Cloudflare Turnstile（action 为 words，hostname 属于 allowed_origins）以及按客户端地址的 PostgreSQL 限流（每 10 分钟 3 次、每天 20 次，只计通过人机验证的请求），然后用仅限 msime-customdict、仅有 contents:write 与 pull_requests:write 的 GitHub App 安装令牌把 `词语<TAB>拼音<TAB>5000` 追加到 words.txt：已有开启的 community-words/* Pull Request 时追加提交，否则从主分支新建 community-words/<UTC yyyymmdd-hhmmss> 分支并开 Pull Request。写入使用文件 blob SHA 做乐观并发，冲突返回 409，服务端从不自动重试 GitHub 写入。备注会公开写入提交说明，@、#、GH- 与 :// 会插入零宽空格。服务不保存访客信息，不记录词条、备注或令牌。',
  'requestBody':{'required':True,'content':{'application/json':{'schema':obj({
    'entries':{'type':'array','minItems':1,'maxItems':20,'items':obj({'word':string(minLength=1,description='1–16 个 CJK 统一表意文字（含各扩展区）或〇，不含字母、数字、标点、空白和控制字符。',example='未来可期'),'pinyin':string(maxLength=200,pattern="^[a-z]+('[a-z]+)*$",description="小写全拼，音节用 ' 连接，音节数等于字数；ü 写作 v，lüe/nüe 写作 lve/nve。音节表与官网表单一致（402 个）。",example="wei'lai'ke'qi")},['word','pinyin'],True)},
    'note':string(maxLength=500,description='可选备注，最多 500 个字符；换行和控制字符会被压成空格。'),
    'token':string(minLength=1,maxLength=2048,description='Turnstile 令牌，一次有效。')},['entries','token'],True)}}},
  'responses':{
    '201':{'description':'已写入 Pull Request','content':{'application/json':{'schema':obj({'pull_request_url':string(format='uri',example='https://github.com/metasequoiaime/msime-customdict/pull/12')},['pull_request_url'])}}},
    '400':word_response('请求无效；词条问题在 rejected 中逐条给出（包括词库中已有的 already_listed）',{'oneOf':[word_rejected,word_error]}),
    '403':word_response('来源不是配置的网站，或 Turnstile 验证失败'),
    '409':word_response('words.txt 被同时修改（或同一秒内新建了同名分支），词条未写入；由用户重新验证后手动重试'),
    '413':word_response('请求体超过 16 KiB'),
    '415':word_response('需要 application/json'),
    '429':word_response('提交过于频繁',retry=True),
    '502':word_response('GitHub 写入结果未知，词条可能已写入；先查看 pulls_url 再决定是否重试',obj({'error':string(),'code':string(enum=['outcome_unknown']),'uncertain':{'type':'boolean','enum':[True]},'pulls_url':string(format='uri')},['error','code','uncertain'])),
    '503':word_response('功能未开启或配置无效、Turnstile/数据库/GitHub 暂不可用（此时尚未写入任何词条）',retry=True)}}}

paths['/v1/telemetry/events']={'post':{
    'summary':'上报安装包下载或崩溃事件','tags':['统计'],
    'security':[{'deviceToken':[]},{'userSession':[]}],
    'description':'需要有效设备或用户令牌、用户数据库及最新迁移。最多 32 KiB。事件 ID 全局唯一，重试复用 ID，重复事件返回 202 且不重复计数。时间以服务端接收时间为准。下载仅为上报计数；download 不得携带 message/stack，crash 必须有 message。不要上传输入内容、密码或个人信息。',
    'requestBody':{'required':True,'content':{'application/json':{'schema':obj({'id':string(minLength=16,maxLength=128),'kind':string(enum=['download','crash']),'platform':string(minLength=1,maxLength=32),'version':string(minLength=1,maxLength=64),'message':string(maxLength=1000),'stack':string(maxLength=16000)},['id','kind','platform','version'],True)}}},
    'responses':{'202':{'description':'已接收（含重复事件）'},'400':{'description':'事件无效'},'401':{'description':'令牌无效'},'415':{'description':'需要 JSON'},'429':{'description':'请求过多'},'503':{'description':'数据库或服务不可用'}}}}

paths['/v1/skins/generate']={'post':{'summary':'生成原创皮肤插画背景','tags':['皮肤'],'description':'只发送风格描述，模型由服务端配置。返回一张 PNG/JPEG，尺寸不超过 2048×2048，图像最多 8 MiB；不保存或自动公开。','requestBody':{'required':True,'content':{'application/json':{'schema':obj({'prompt':string(minLength=1,maxLength=1200)},['prompt'],True)}}},'responses':{'200':{'description':'生成成功','content':{'application/json':{'schema':obj({'b64_json':string(format='byte'),'mime_type':string(enum=['image/png','image/jpeg']),'width':{'type':'integer'},'height':{'type':'integer'}})}}},'400':{'description':'描述无效'},'401':{'description':'需要认证'},'502':{'description':'生成结果无效'},'503':{'description':'未配置或繁忙'}}}}

artwork_schema=paths['/v1/skins/generate']['post']['responses']['200']['content']['application/json']['schema']
job_id=string(pattern='^[0-9a-f]{48}$')
job_status=obj({'id':job_id,'state':string(enum=['running','succeeded','failed']),'artwork':dict(artwork_schema,nullable=True)},['id','state'])
paths['/v1/skins/jobs']={'post':{'summary':'提交原创皮肤插画任务','tags':['皮肤'],'description':'立即返回任务 ID，不等待生图。任务仅当前认证主体可访问；每主体最多 3 个任务，全局最多 min(8,max_concurrent) 个。草稿保留 10 分钟，服务重启会失效；不是持久化皮肤。领取结果后应 DELETE 释放。不要自动重试提交，避免重复生图。','requestBody':paths['/v1/skins/generate']['post']['requestBody'],'responses':{'202':{'description':'已创建','content':{'application/json':{'schema':obj({'id':job_id,'state':string(enum=['running']),'expires_at':string(format='date-time')},['id','state','expires_at'])}}},'400':{'description':'描述无效'},'401':{'description':'需要认证'},'503':{'description':'未配置、容量已满或正在关闭'}}}}
paths['/v1/skins/jobs/{job}']={'parameters':[{'name':'job','in':'path','required':True,'schema':job_id}],
'get':{'summary':'查询插画任务及领取结果','tags':['皮肤'],'description':'running 时每 5 秒查询一次；succeeded 返回有界 PNG/JPEG 的 base64 数据；failed 不返回上游细节。不同主体、已删除、重启或过期的任务均返回 404。','responses':{'200':{'description':'任务状态','content':{'application/json':{'schema':job_status}}},'401':{'description':'需要认证'},'404':{'description':'任务不存在或已失效'}}},
'delete':{'summary':'取消或释放插画任务','tags':['皮肤'],'responses':{'204':{'description':'已释放，运行中的上游请求已取消'},'401':{'description':'需要认证'},'404':{'description':'任务不存在或不属于当前主体'}}}}

output=root/'internal/server/swagger/openapi.json'
data=json.dumps(result,ensure_ascii=False,indent=2)+'\n'
if '--check' in sys.argv:
    if not output.exists() or output.read_text()!=data:sys.exit('OpenAPI 未同步：请运行 python3 scripts/generate_openapi.py')
else:output.write_text(data)
print('OpenAPI 与契约一致' if '--check' in sys.argv else '已生成 OpenAPI')
