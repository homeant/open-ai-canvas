package app

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"yingce/backend/internal/model"
)

const qwenAudioDefaultVoice = "longanhuan_v3.1"

var qwenAudioVoiceID = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// The IDs and labels follow EvoLink's Qwen Audio 3.1 TTS Flash voice catalog.
var qwenAudioVoices = []struct{ ID, Label, Group string }{
	{"longanhuan_v3.1", "龙安欢", "多语种"},
	{"longanlingxin_v3.1", "龙安灵心", "多语种"},
	{"longanfengyue_v3.1", "龙安风悦", "多语种"},
	{"xunanchuan_v3.1", "许南川", "多语种"},
	{"yuxiaoyun_v3.1", "于小云", "中文"},
	{"qiaoxiaojiao_v3.1", "乔小娇", "中文"},
	{"xiaxiaochen_v3.1", "夏小晨", "中文"},
	{"anmingyuan_v3.1", "安明远", "中文"},
	{"wenhuaiqing_v3.1", "温怀清", "中文"},
	{"anxiaolan_v3.1", "安小岚", "中文"},
	{"xieshurou_v3.1", "谢舒柔", "中文"},
	{"baiqinglan_v3.1", "白清岚", "中文"},
	{"xuyuyuan_v3.1", "许玉远", "中文"},
	{"anruorou_v3.1", "安若柔", "中文"},
	{"wenhuaizhi_v3.1", "闻怀之", "中文"},
	{"xiaoxingzhi_v3.1", "萧行之", "中文"},
	{"guyunshu_v3.1", "顾云舒", "中文"},
	{"huozhuoshi_v3.1", "霍拙石", "中文"},
	{"yeqinghe_v3.1", "叶清禾", "中文"},
	{"yunhuanhuan_v3.1", "云欢欢", "中文"},
	{"xuxiaoqiao_v3.1", "徐小俏", "中文"},
	{"baianran_v3.1", "白安然", "中文"},
	{"xuyanchu_v3.1", "许言初", "中文"},
	{"yezhiqing_v3.1", "叶知晴", "中文"},
	{"andi_v3.1", "安迪", "中文"},
	{"anyuqing_v3.1", "安语晴", "中文"},
	{"Emily_v3.1", "Emily", "英文"},
	{"Luna_v3.1", "Luna", "英文"},
	{"Eric_v3.1", "Eric", "英文"},
	{"Luca_v3.1", "Luca", "英文"},
	{"Abby_v3.1", "Abby", "英文"},
	{"Annie_v3.1", "Annie", "英文"},
	{"Ava_v3.1", "Ava", "英文"},
	{"Beth_v3.1", "Beth", "英文"},
	{"Betty_v3.1", "Betty", "英文"},
	{"Cally_v3.1", "Cally", "英文"},
	{"Cindy_v3.1", "Cindy", "英文"},
	{"Donna_v3.1", "Donna", "英文"},
	{"Andy_v3.1", "Andy", "英文"},
	{"Brian_v3.1", "Brian", "英文"},
	{"David_v3.1", "David", "英文"},
	{"longanyuanfei_v3.1", "龙安元妃", "其他"},
	{"longjielidou_v3.1", "龙杰力豆", "其他"},
	{"longanlingxi_v3.1", "龙安灵希", "其他"},
	{"longhuohuo_v3.1", "龙火火", "其他"},
	{"longyingtao_v3.1", "龙应桃", "其他"},
	{"longanya_v3.1", "龙安雅", "其他"},
	{"longwan_v3.1", "龙婉", "其他"},
	{"longxing_v3.1", "龙星", "其他"},
	{"longhua_v3.1", "龙华", "其他"},
	{"longhan_v3.1", "龙寒", "其他"},
	{"longanzhi_v3.1", "龙安智", "其他"},
	{"longzhe_v3.1", "龙哲", "其他"},
	{"longanyang_v3.1", "龙安洋", "其他"},
	{"libai_v3.1", "李白", "其他"},
	{"longling_v3.1", "龙铃", "其他"},
	{"longniuniu_v3.1", "龙牛牛", "其他"},
	{"longshanshan_v3.1", "龙闪闪", "其他"},
	{"longpaopao_v3.1", "龙泡泡", "其他"},
	{"loongstella_v3.1", "loongstella", "其他"},
	{"longyuan_v3.1", "龙媛", "其他"},
	{"longmiao_v3.1", "龙妙", "其他"},
	{"longsanshu_v3.1", "龙三叔", "其他"},
	{"longanli_v3.1", "龙安莉", "其他"},
	{"longanwen_v3.1", "龙安温", "其他"},
	{"longanlang_v3.1", "龙安朗", "其他"},
	{"longxiaoxia_v3.1", "龙小夏", "其他"},
	{"longanchong_v3.1", "龙安冲", "其他"},
}

func cloudAgentQwenAudioSettings() map[string]any {
	voices := make([]map[string]string, 0, len(qwenAudioVoices))
	for _, voice := range qwenAudioVoices {
		voices = append(voices, map[string]string{"value": voice.ID, "label": voice.Label, "group": voice.Group})
	}
	return map[string]any{
		"voice":            map[string]any{"default": qwenAudioDefaultVoice, "voices": voices, "customVoiceIdAllowed": true, "caseSensitive": true},
		"voiceInstruction": map[string]any{"default": "", "maxBillingCharacters": 100, "hanCharacterWeight": 2, "otherCharacterWeight": 1},
		"prompt":           map[string]any{"purpose": "只填写实际朗读文本", "maxCharacters": 5000},
	}
}

func qwenAudioInstructionBillingCharacters(value string) int {
	count := 0
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			count += 2
		} else {
			count++
		}
	}
	return count
}

func (s *Service) validateCloudAgentQwenAudio(a *cloudAgentMediaArgs) error {
	if a.Mode != "audio" || a.ChannelID == "" || a.ChannelModelKey == "" {
		return nil
	}
	selected, err := s.repo.ChannelModelByKey(a.ChannelID, a.ChannelModelKey)
	if err != nil {
		return err
	}
	if selected.Protocol != model.ChannelInterfaceEvoLinkQwenAudioTTS {
		return nil
	}
	if utf8.RuneCountInString(a.Prompt) > 5000 {
		return cloudAgentFieldError("prompt", "length_exceeded", "Qwen TTS 朗读文本最多 5000 字符")
	}
	if len(a.ReferenceNodeIDs) > 0 || len(a.ReferenceTransientIDs) > 0 || a.SourceNodeID != "" {
		return cloudAgentFieldError("referenceNodeIds", "invalid_value", "Qwen TTS 的 prompt 只接受朗读文本，不接收参考节点或角色设定")
	}
	a.Voice = strings.TrimSpace(a.Voice)
	if a.Voice == "" {
		a.Voice = qwenAudioDefaultVoice
	}
	if !qwenAudioVoiceID.MatchString(a.Voice) {
		return cloudAgentFieldError("voice", "invalid_value", "音色 ID 格式无效，请复制 model_list 返回的 ID 或当前账号已有的自定义音色 ID")
	}
	a.VoiceInstruction = strings.TrimSpace(a.VoiceInstruction)
	if qwenAudioInstructionBillingCharacters(a.VoiceInstruction) > 100 {
		return cloudAgentFieldError("voiceInstruction", "length_exceeded", "Qwen TTS 声音指令最多 100 计费字符：汉字按 2 计，其他字符按 1 计")
	}
	return nil
}
