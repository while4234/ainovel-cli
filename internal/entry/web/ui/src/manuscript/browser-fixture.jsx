import React, { useState } from 'react';
import { createRoot } from 'react-dom/client';
import { ManuscriptWorkspace } from './ManuscriptWorkspace.jsx';
import { FoundationCenter } from '../foundation/FoundationCenter.jsx';
import { ArtworkWorkspace } from '../artwork/ArtworkWorkspace.jsx';

function BrowserFixture() {
  const surface = new URLSearchParams(globalThis.location.search).get('surface');
  const foundationSurface = surface === 'foundation';
  const artworkSurface = surface === 'artwork';
  const [controlsTarget, setControlsTarget] = useState(null);
  const [projectId, setProjectId] = useState(foundationSurface ? 'foundation-project-a' : 'browser-project');
  const [notice, setNotice] = useState('');
  if (artworkSurface) {
    return <div className="artwork-browser-fixture" style={{ display: 'flex', flexDirection: 'column', height: '100vh', minHeight: 0, overflow: 'hidden' }}>
      <ArtworkWorkspace
        modelConfig={{ providers: [{ name: 'fixture-text', label: 'Fixture 文本模型', models: ['fixture-writer'] }], roles: [{ role: 'default', provider: 'fixture-text', model: 'fixture-writer' }] }}
        onClose={() => setNotice('返回创作工作台')}
        onDirtyChange={() => {}}
        onPromptModelChange={() => Promise.resolve()}
        projectId="artwork-browser-project"
        projectName="雾港纪事"
        runtime={{ config: { provider: 'fixture-text', model: 'fixture-writer' } }}
      />
      <div aria-live="polite" className="artwork-fixture-notice" role="status">{notice}</div>
    </div>;
  }
  if (foundationSurface) {
    return <div className="foundation-browser-fixture" style={{ display: 'flex', flexDirection: 'column', height: '100vh', minHeight: 0, overflow: 'hidden' }}>
      <div className="inline-actions"><button type="button" onClick={() => setProjectId('foundation-project-a')}>项目 A</button><button type="button" onClick={() => setProjectId('foundation-project-b')}>项目 B</button></div>
      <div aria-live="polite" role="status">{notice}</div>
      <FoundationCenter projectId={projectId} onClose={() => setNotice('返回创作')} onOpenCoreCast={() => setNotice('打开现有 CoreCast 确认')} onOpenReview={(mode) => setNotice(`打开现有 ${mode} 审核`)} />
    </div>;
  }
  return <><aside ref={setControlsTarget} /><ManuscriptWorkspace active controlsTarget={controlsTarget} projectId="browser-project" /></>;
}

createRoot(document.getElementById('root')).render(<BrowserFixture />);
