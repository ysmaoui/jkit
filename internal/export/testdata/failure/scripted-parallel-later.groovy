node { parallel X: { stage('X1') { catchError(buildResult: 'FAILURE', stageResult: 'FAILURE') { sleep 1; error 'caught' } } },
 Y: { stage('Y1') { sleep 4; error 'real' } } }
