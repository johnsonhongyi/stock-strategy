import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { PortfolioInspectionJob } from '../lib/backend';
import { PortfolioInspectionWorkspace, PortfolioReportView } from './PortfolioInspectionWorkspace';

describe('PortfolioInspectionWorkspace - Audit & Re-inspection', () => {
	const sampleJob: PortfolioInspectionJob = {
		id: 'job-101',
		status: 'succeeded',
		stage: 'completed',
		completed_stocks: 1,
		total_stocks: 1,
		coverage_percent: 100,
		current_symbols: [],
		message: '巡检完成',
		report_available: true,
		started_at: '2026-10-22T09:30:00Z',
		updated_at: '2026-10-22T10:00:00Z',
		request: {
			trader_profile: 'aggressive',
			holdings: [{ symbol: '600519.SH', name: '贵州茅台', weight_percent: 50, cost_price: 1800 }],
			ai_provider: 'codex_desktop',
			model: 'gpt-6-luna',
			reasoning_effort: 'high',
			previous_inspection_id: 'job-100',
		},
		results: [{
			holding: { symbol: '600519.SH', name: '贵州茅台', weight_percent: 50, cost_price: 1800 },
			status: 'succeeded',
		}],
		report: {
			id: 'job-101',
			previous_inspection_id: 'job-100',
			audit_base_date: '2026-10-21 15:00',
			prompt_version: 'portfolio-inspection-v2',
			algorithm_version: 'portfolio-health-v2',
			profile: {
				id: 'aggressive',
				label: '激进',
				description: '短线弹性优先',
				max_single_percent: 45,
				max_top_three_percent: 90,
				minimum_cash_percent: 0,
				max_high_risk_percent: 75,
				max_stop_loss_risk_percent: 8,
				preferred_short_term_max_percent: 80,
			},
			holdings: [{
				holding: { symbol: '600519.SH', name: '贵州茅台', weight_percent: 50, cost_price: 1800 },
				status: 'succeeded',
			}],
			metrics: {
				total_position_percent: 50,
				cash_percent: 50,
				coverage_percent: 100,
				max_single_percent: 50,
				top_three_percent: 50,
				concentration_hhi: 2500,
				weighted_stock_score: 75,
				weighted_risk_score: 30,
				stop_loss_risk_percent: 2.5,
				stop_loss_coverage_percent: 100,
				ai_research_coverage_percent: 100,
				short_term_percent: 50,
				new_listing_percent: 0,
				high_risk_percent: 0,
				health_score: 85,
				health_score_available: true,
				risk_resilience_score: 90,
				diversification_score: 80,
				style_match_score: 95,
				style_breaches: [],
				theme_exposures: [],
				high_correlations: [],
				risk_contributions: [{ symbol: '600519.SH', name: '贵州茅台', weight_percent: 50, score: 75, contribution_percent: 50 }],
			},
			conclusion: {
				health_score: 85,
				risk_level: '低',
				style_match: '匹配',
				executive_summary: '新交易日走势良好，茅台放量站稳1850',
				audit_summary: '对比 2026-10-21 巡检评估：新交易日茅台放量站稳1850兑现确认条件，持仓结构健康。',
				primary_risks: [],
				concentration_findings: [],
				holdings: [{
					symbol: '600519.SH',
					portfolio_role: '核心',
					risk_contribution: 50,
					conclusion: '短线突破有效，保持进攻仓位',
					action_priority: '保持',
					action: '持股并继续跟踪',
					confirmation: '站稳1850',
					invalidation: '跌破1800',
					previous_action: '观察突破1850',
					trend_audit: '新交易日放量突破1850，符合前次预案，未触发失效线',
				}],
				adjustment_order: ['暂无必须立即处理事项'],
				scenarios: [{ name: '市场增强', condition: '量价齐升', portfolio_action: '继续持有' }],
				next_checklist: ['观察1850支撑有效性'],
				data_limitations: [],
				confidence: 0.9,
				source: 'codex-desktop-ai',
			},
			generated_at: '2026-10-22T10:00:00Z',
		},
	};

	it('renders update inspection, load draft and new inspection actions in report view', () => {
		const markup = renderToStaticMarkup(<PortfolioReportView
			job={sampleJob}
			report={sampleJob.report!}
			status={sampleJob.status}
			onUpdate={() => {}}
			onLoadToDraft={() => {}}
			onNew={() => {}}
			onOpenStockAnalysis={() => {}}
		/>);

		// 检查操作按钮入口
		expect(markup).toContain('更新巡检（审核新走势）');
		expect(markup).toContain('载入此配置');
		expect(markup).toContain('新建巡检');

		// 检查新交易日走势审核 Banner
		expect(markup).toContain('新交易日走势审核与策略更新');
		expect(markup).toContain('对比 2026-10-21 巡检评估：新交易日茅台放量站稳1850兑现确认条件');

		// 检查逐股前次建议动作与新走势审核内容
		expect(markup).toContain('前次建议动作：');
		expect(markup).toContain('观察突破1850');
		expect(markup).toContain('新走势审核：');
		expect(markup).toContain('新交易日放量突破1850，符合前次预案，未触发失效线');
	});

	it('renders default setup form when no active report is selected', () => {
		const markup = renderToStaticMarkup(<PortfolioInspectionWorkspace
			config={null}
			refreshKey={1}
			onOpenSettings={() => {}}
			onOpenStockAnalysis={() => {}}
		/>);
		expect(markup).toContain('开始 AI 巡检');
		expect(markup).toContain('交易风格');
	});
});
