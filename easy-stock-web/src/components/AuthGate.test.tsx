import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { AuthGate } from './AuthGate';

describe('AuthGate', () => {
    it('renders the initial auth gate card when status is loading', () => {
        const markup = renderToStaticMarkup(
            <AuthGate>
                <div data-testid="protected-content">受保护的量化平台</div>
            </AuthGate>
        );

        expect(markup).toContain('Easy Stock 身份验证');
        expect(markup).toContain('重试连接服务');
        expect(markup).not.toContain('受保护的量化平台');
    });
});
