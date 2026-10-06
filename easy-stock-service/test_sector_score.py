"""P26 板块四维确认测试。"""
import sys, os
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from sector_leader import score_dims, sector_score

ok = 0
def check(name, cond):
    global ok
    assert cond, "FAIL: " + name
    ok += 1
    print("ok:", name)

# 1. 强板块:满分100
r = score_dims([2.5, 3.1, 1.8, 4.0], 3, "L3", 1.6)
check("强板块满分", r["score"] == 100 and r["强度"] == 25 and r["热度"] == 25 and r["力度"] == 25 and r["量能"] == 25)

# 2. 弱板块:0分
r = score_dims([-1.2, -0.5, -2.0], 0, None, 0.8)
check("弱板块0分", r["score"] == 0)

# 3. 边界:中位数1.0->15,1个涨停->15,L2->15,量比1.2->15,共60
r = score_dims([1.0, 1.5, 0.8], 1, "L2", 1.2)
check("边界60分", r["score"] == 60)

# 4. 中位数0.3->8,量比1.0->8
r = score_dims([0.3, 0.5, -0.2], 0, "L1", 1.0)
check("中庸24分", r["score"] == 8 + 0 + 8 + 8)

# 5. 空数据->0分不崩
r = score_dims([], 0, None, None)
check("空数据0分", r["score"] == 0)

# 6. fail-closed:空板块/未知板块
r = sector_score("")
check("空板块fail-closed", r["score"] == 0)
r = sector_score("不存在的板块XYZ")
check("未知板块fail-closed", r["score"] == 0)

print("全部通过: %d项" % ok)
