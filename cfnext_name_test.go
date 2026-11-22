package main

// 面板节点名生成规则：必须与页面导出的一行 `IP:PORT#...` 完全一致。

import "testing"

func TestResolveCFNextName(t *testing.T) {
	cases := []struct {
		desc string
		res  iptestResult
		exp  exportConfig
		want string
	}{
		{
			desc: "后缀 dc + IP类型",
			res:  iptestResult{dataCenter: "LAX", ipType: "IPv4", visitScheme: "https"},
			exp:  exportConfig{SuffixMode: "dc", Separator: "-", Fields: []string{"ipport", "dc", "ipType"}},
			want: "LAX-IPv4",
		},
		{
			desc: "IP类型为空时自动省略该节",
			res:  iptestResult{dataCenter: "LAX"},
			exp:  exportConfig{SuffixMode: "dc", Separator: "-", Fields: []string{"ipport", "dc", "ipType"}},
			want: "LAX",
		},
		{
			desc: "固定文本含 {dc} 占位 + 自定义分隔符",
			res:  iptestResult{dataCenter: "SJC", ipType: "IPv6"},
			exp:  exportConfig{SuffixMode: "custom", SuffixText: "HK-{dc}", Separator: "_", Fields: []string{"ipport", "dc", "ipType"}},
			want: "HK-SJC_IPv6",
		},
		{
			desc: "不加后缀时 dc 作为普通字段",
			res:  iptestResult{dataCenter: "SEA"},
			exp:  exportConfig{SuffixMode: "none", Separator: "-", Fields: []string{"ipport", "dc"}},
			want: "SEA",
		},
		{
			desc: "只勾 IP:PORT 时回退数据中心",
			res:  iptestResult{dataCenter: "NRT"},
			exp:  exportConfig{SuffixMode: "dc", Fields: []string{"ipport"}},
			want: "NRT",
		},
		{
			desc: "延迟与速度按分隔符拼接",
			res:  iptestResult{dataCenter: "LAX", latency: "30ms", speedText: "125.4MB/s"},
			exp:  exportConfig{SuffixMode: "dc", Separator: "-", Fields: []string{"ipport", "dc", "latency", "speed"}},
			want: "LAX-30ms-125.4MB/s",
		},
		{
			desc: "重复值只保留一次",
			res:  iptestResult{dataCenter: "LAX", city: "LAX"},
			exp:  exportConfig{SuffixMode: "dc", Separator: "-", Fields: []string{"ipport", "dc", "city"}},
			want: "LAX",
		},
	}
	for _, c := range cases {
		got := resolveCFNextName(c.res, c.exp, "CF")
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.desc, got, c.want)
		}
	}
}
