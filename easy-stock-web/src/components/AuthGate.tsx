import { useCallback, useEffect, useState, type ReactNode, type FormEvent } from 'react';
type User = {
    id: number;
    username: string;
    admin: boolean;
};
type Status = {
    authenticated: boolean;
    lan: boolean;
    user: User;
};
async function api<T>(path: string, body?: unknown): Promise<T> {
    const response = await fetch(`/api/auth/${path}`, { method: body === undefined ? 'GET' : 'POST', headers: body === undefined ? {} : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body), credentials: 'same-origin' });
    const data = await response.json();
    if (!response.ok)
        throw new Error(data.error || '请求失败');
    return data;
}
export function AuthGate({ children }: {
    children: ReactNode;
}) {
    const [status, setStatus] = useState<Status>();
    const [panel, setPanel] = useState(false);
    const [forceLogin, setForceLogin] = useState(false);
    const [users, setUsers] = useState<User[]>([]);
    const [error, setError] = useState('');
    const [message, setMessage] = useState('');
    const [busy, setBusy] = useState(false);
    const refresh = useCallback(async () => { try {
        setStatus(await api<Status>('status'));
        setError('');
    }
    catch (e) {
        setError(String(e));
    } }, []);
    useEffect(() => { void refresh(); const expired = () => { setStatus(undefined); void refresh(); }; window.addEventListener('stock-auth-required', expired); const timer = window.setInterval(() => void refresh(), 60000); return () => { window.removeEventListener('stock-auth-required', expired); clearInterval(timer); }; }, [refresh]);
    useEffect(() => { if (panel && status?.user.admin)
        api<User[]>('users').then(setUsers).catch(e => setError(String(e))); }, [panel, status]);
    async function submit(event: FormEvent<HTMLFormElement>, action: string) {
        event.preventDefault();
        const form = event.currentTarget;
        const data = new FormData(form);
        setBusy(true);
        setError('');
        setMessage('');
        try {
            await api(action, { username: data.get('username'), password: data.get('password'), old_password: data.get('old_password'), admin: data.get('admin') === 'on' });
            form.reset();
            if (action === 'login') {
                setForceLogin(false);
                await refresh();
            }
            if (action === 'password') {
                setForceLogin(true);
                setPanel(false);
                await refresh();
                setMessage('密码已修改，请重新登录');
            }
            if (action === 'reset-password') {
                await refresh();
                setMessage('密码已重置，该用户须重新登录');
            }
            if (action === 'users') {
                setUsers(await api<User[]>('users'));
                setMessage('用户已添加');
            }
        }
        catch (e) {
            setError(e instanceof Error ? e.message : String(e));
        }
        finally {
            setBusy(false);
        }
    }
    const field = { display: 'block', width: '100%', padding: 10, margin: '8px 0', color: '#162238', background: '#fff', border: '1px solid #aab5c8', borderRadius: 6 };
    const card = { background: '#fff', color: '#162238', padding: 24, borderRadius: 12, width: 420, maxWidth: '95vw', boxShadow: '0 8px 40px #0004' };
    const notices = <>{error && <p role="alert" style={{ color: '#b42318' }}>{error}</p>}{message && <p role="status">{message}</p>}</>;
    if (!status || (!status.authenticated && (!status.lan || forceLogin)))
        return <div style={{ minHeight: '100vh', display: 'grid', placeItems: 'center', background: '#eaf0f8' }}><section style={card}><h1>Easy Stock 登录</h1>{notices}{!status ? <button onClick={() => void refresh()}>重试连接</button> : <form onSubmit={e => void submit(e, 'login')}><input style={field} name="username" aria-label="用户名" placeholder="用户名" autoComplete="username" required maxLength={64}/><input style={field} name="password" aria-label="密码" placeholder="密码" type="password" autoComplete="current-password" required maxLength={256}/><button disabled={busy}>登录</button>{status.lan && <button type="button" onClick={() => setForceLogin(false)}>返回内网页面</button>}</form>}</section></div>;
    return <>{children}<button style={{ position: 'fixed', bottom: 12, right: 16, zIndex: 1000 }} onClick={() => { if (!status.authenticated)
        setForceLogin(true);
    else
        setPanel(true); }}>{status.authenticated ? `${status.user.username} · 用户管理` : '内网免登录 · 账号管理'}</button>{panel && <div style={{ position: 'fixed', inset: 0, background: '#0007', display: 'grid', placeItems: 'center', zIndex: 2000 }}><section role="dialog" aria-modal="true" aria-label="用户管理" style={{ ...card, maxHeight: '90vh', overflow: 'auto' }}><button onClick={() => setPanel(false)}>关闭</button><h2>账号管理</h2>{notices}<h3>修改我的密码</h3><form onSubmit={e => void submit(e, 'password')}><input style={field} name="old_password" aria-label="原密码" type="password" placeholder="原密码" autoComplete="current-password" required/><input style={field} name="password" aria-label="新密码" type="password" placeholder="新密码（至少8位）" autoComplete="new-password" minLength={8} maxLength={256} required/><button disabled={busy}>修改密码</button></form>{status.user.admin && <><h3>用户列表</h3><ul>{users.map(u => <li key={u.id}>{u.username} · {u.admin ? '管理员' : '普通用户'}</li>)}</ul><h3>重置用户密码</h3><form onSubmit={e => void submit(e, 'reset-password')}><select style={field} name="username" aria-label="选择用户">{users.map(u => <option key={u.id} value={u.username}>{u.username}</option>)}</select><input style={field} name="password" aria-label="重置密码" type="password" placeholder="新密码（至少8位）" autoComplete="new-password" minLength={8} maxLength={256} required/><button disabled={busy}>重置密码</button></form><h3>添加用户</h3><form onSubmit={e => void submit(e, 'users')}><input style={field} name="username" aria-label="新用户名" placeholder="用户名" maxLength={64} autoComplete="off" required/><input style={field} name="password" aria-label="初始密码" type="password" placeholder="初始密码（至少8位）" autoComplete="new-password" minLength={8} maxLength={256} required/><label><input name="admin" type="checkbox"/> 管理员</label><button disabled={busy}>添加用户</button></form></>}<button disabled={busy} onClick={async () => { setBusy(true); try {
        await api('logout', {});
        setPanel(false);
        await refresh();
    }
    catch (e) {
        setError(String(e));
    }
    finally {
        setBusy(false);
    } }}>退出登录</button></section></div>}</>;
}
