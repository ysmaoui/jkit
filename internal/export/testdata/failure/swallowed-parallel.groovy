node { stage('A') { catchError(buildResult: 'FAILURE', stageResult: 'FAILURE') { error 'caught' } }
 stage('P') { try { parallel Y: { stage('Y1') { error 'swallowed' } }, Z: { stage('Z1') { echo 'z' } } } catch (e) { echo 'swallowed' } }
 stage('B') { error 'real' } }
