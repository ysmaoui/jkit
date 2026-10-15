node { parallel X: { stage('X1') { sleep 3; catchError(buildResult: 'FAILURE', stageResult: 'FAILURE') { error 'caught' } } },
 Y: { stage('Y1') { sleep 1; error 'real' } } }
