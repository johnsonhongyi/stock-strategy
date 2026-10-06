"""重点个股自动配图:复用 review_charts.draw,为推送生成K线图。
失败不抛异常,返回成功生成的路径列表(推送方可直接忽略空列表)。"""
import os


def draw_list(pairs, outdir, max_n=3):
    """pairs: [(6位代码, 名称), ...] -> [png路径]。最多 max_n 只。"""
    os.makedirs(outdir, exist_ok=True)
    imgs = []
    try:
        from review_charts import draw as draw_chart
    except Exception as e:
        print("chart module fail: %s" % e, flush=True)
        return imgs
    for code, name in pairs[:max_n]:
        try:
            draw_chart(code, name, outdir)
            p = os.path.join(outdir, code + ".png")
            if os.path.exists(p) and os.path.getsize(p) < 2 * 1024 * 1024:
                imgs.append(p)
        except Exception as e:
            print("chart fail %s: %s" % (code, e), flush=True)
    return imgs
